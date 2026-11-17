package main

// patterns modes that probe what the earlier experiments left out (docs/EXPERIMENT_PLAN.md, "deepen" phase):
//
//	reboot-recover  reboot the writer under steady load: how long are queries failing, with which errors, how does the pool recover
//	limit           where the limits are: max_connections straight on Aurora, many idle clients on the Proxy, busy clients beyond the Proxy's
//	                backend limit (the borrow timeout)
//	contention      when the pool, not the CPU, is the limit: hot-row updates, transactions that hold a connection
//	mixed           a production-like mix: slow workers, fast workers, web requests of several queries, one GET_LOCK connection per site

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
)

// errLabel turns an error into a short, stable label (so counts can be added up and read).
func errLabel(err error) string {
	if err == nil {
		return "ok"
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return fmt.Sprintf("mysql-%d", me.Number)
	}
	s := err.Error()
	for _, k := range []string{"bad connection", "invalid connection", "connection refused", "EOF", "broken pipe", "reset by peer", "deadline exceeded", "context canceled", "i/o timeout", "closed"} {
		if strings.Contains(s, k) {
			return strings.ReplaceAll(k, " ", "-")
		}
	}
	s = strings.Join(strings.Fields(s), "_")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func kindsString(m map[string]int) string {
	type kv struct {
		k string
		n int
	}
	var l []kv
	for k, n := range m {
		l = append(l, kv{k, n})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].n > l[j].n })
	var out []string
	for _, x := range l {
		out = append(out, fmt.Sprintf("%s:%d", x.k, x.n))
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, "|")
}

// ---- reboot-recover ---------------------------------------------------------------------------------------------------------

func patRebootRecover(ctx context.Context, t target, tlsF string, workers int, think, rebootAfter, lifetime time.Duration, rebootCmd string, dur time.Duration) error {
	db, cc, err := newPatDB(t, poolOpts{maxOpen: workers, maxIdle: workers, interpolate: true, tls: tlsF, lifetime: lifetime}, nil)
	if err != nil {
		return err
	}
	defer db.Close()
	start := time.Now()
	end := start.Add(dur - 15*time.Second)
	var mu sync.Mutex
	okSec, errSec := map[int]int{}, map[int]int{}
	kinds := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(end) && ctx.Err() == nil {
				qctx, cancel := context.WithTimeout(ctx, 5*time.Second) // a query must not hang for the driver's 60 s read timeout
				var x int
				err := db.QueryRowContext(qctx, "SELECT 1").Scan(&x)
				cancel()
				sec := int(time.Since(start).Seconds())
				mu.Lock()
				if err == nil {
					okSec[sec]++
				} else {
					errSec[sec]++
					kinds[errLabel(err)]++
				}
				mu.Unlock()
				time.Sleep(think)
			}
		}()
	}
	rebootAt := -1
	go func() {
		select {
		case <-time.After(rebootAfter):
		case <-ctx.Done():
			return
		}
		rebootAt = int(time.Since(start).Seconds())
		out, err := exec.Command("sh", "-c", rebootCmd).CombinedOutput()
		log.Printf("PATTERN-REBOOT issued at t=%ds: err=%v out=%.120s", rebootAt, err, strings.Join(strings.Fields(string(out)), " "))
	}()
	wg.Wait()
	// first failing second, and the first second after which 5 seconds in a row had successes and no errors
	total := int(dur.Seconds())
	firstErr, recovered := -1, -1
	for s := 0; s < total; s++ {
		if errSec[s] > 0 && firstErr < 0 {
			firstErr = s
		}
		if firstErr >= 0 && recovered < 0 && s > firstErr {
			good := true
			for k := 0; k < 5; k++ {
				if errSec[s+k] > 0 || okSec[s+k] == 0 {
					good = false
					break
				}
			}
			if good {
				recovered = s
			}
		}
	}
	var okN, errN int
	for _, n := range okSec {
		okN += n
	}
	for _, n := range errSec {
		errN += n
	}
	for s := 0; s < total; s++ {
		if okSec[s]+errSec[s] > 0 {
			log.Printf("PATTERN-SECOND t=%d ok=%d err=%d", s, okSec[s], errSec[s])
		}
	}
	outage := -1
	if firstErr >= 0 && recovered >= 0 {
		outage = recovered - firstErr
	}
	result("mode", "reboot-recover", "workers", workers, "lifetime_s", lifetime.Seconds(), "reboot_issued_at_s", rebootAt, "first_error_at_s", firstErr,
		"recovered_at_s", recovered, "outage_s", outage, "ok", okN, "errors", errN, "error_kinds", kindsString(kinds), "new_connections", cc.n.Load())
	return nil
}

// ---- limit ------------------------------------------------------------------------------------------------------------------

func patLimit(ctx context.Context, t target, tlsF, variant string, n int, hold time.Duration) error {
	db, _, err := newPatDB(t, poolOpts{maxOpen: n + 5, maxIdle: n + 5, interpolate: true, tls: tlsF}, nil)
	if err != nil {
		return err
	}
	defer db.Close()
	switch variant {
	case "direct-exhaust", "idle-clients":
		// open n connections, one after the other, and hold them (direct-exhaust is meant for --target=cluster: Aurora's max_connections)
		var conns []*sql.Conn
		kinds := map[string]int{}
		failedAt := -1
		for i := 0; i < n && ctx.Err() == nil; i++ {
			c, err := db.Conn(ctx)
			if err == nil {
				var x int
				err = c.QueryRowContext(ctx, "SELECT 1").Scan(&x)
			}
			if err != nil {
				kinds[errLabel(err)]++
				if failedAt < 0 {
					failedAt = i
				}
				continue
			}
			conns = append(conns, c)
			time.Sleep(20 * time.Millisecond)
		}
		opened := len(conns)
		log.Printf("PATTERN-LIMIT opened=%d of %d; holding for %s", opened, n, hold)
		end := time.Now().Add(hold)
		for time.Now().Before(end) && ctx.Err() == nil {
			time.Sleep(20 * time.Second)
			for _, c := range conns { // idle clients ping now and then, like a pool keepalive
				_ = c.PingContext(ctx)
			}
		}
		for _, c := range conns {
			_ = c.Close()
		}
		result("mode", "limit", "variant", variant, "wanted", n, "opened", opened, "failed", n-opened, "first_failure_at_connection", failedAt, "error_kinds", kindsString(kinds))
	case "busy-clients":
		// n clients each run a query that keeps its backend connection busy for `hold`. Beyond the Proxy's backend limit a client waits for a
		// connection; after the Proxy's BorrowTimeout (30 s) it gets an error.
		var mu sync.Mutex
		kinds := map[string]int{}
		var okN, errN int
		var errAfter []time.Duration
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				t0 := time.Now()
				qctx, cancel := context.WithTimeout(ctx, hold+60*time.Second)
				defer cancel()
				var x int
				err := db.QueryRowContext(qctx, fmt.Sprintf("SELECT SLEEP(%.0f)", hold.Seconds())).Scan(&x)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errN++
					kinds[errLabel(err)]++
					errAfter = append(errAfter, time.Since(t0))
				} else {
					okN++
				}
			}()
			time.Sleep(50 * time.Millisecond)
		}
		wg.Wait()
		result("mode", "limit", "variant", variant, "clients", n, "hold_s", hold.Seconds(), "ok", okN, "errors", errN, "error_kinds", kindsString(kinds),
			"error_after_p50_s", quantile(errAfter, 0.5).Seconds(), "error_after_max_s", quantile(errAfter, 1).Seconds())
	default:
		return fmt.Errorf("--variant must be direct-exhaust | idle-clients | busy-clients")
	}
	return nil
}

// ---- contention -------------------------------------------------------------------------------------------------------------

func patContention(ctx context.Context, t target, tlsF, variant string, workers, maxOpen int, hold time.Duration, dur time.Duration, rows int) error {
	if err := capSetup(ctx, t, rows); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	db, _, err := newPatDB(t, poolOpts{maxOpen: maxOpen, maxIdle: maxOpen, interpolate: true, tls: tlsF}, nil)
	if err != nil {
		return err
	}
	defer db.Close()
	var mu sync.Mutex
	var lat []time.Duration
	kinds := map[string]int{}
	var ok, bad atomic.Int64
	var inUseSum, inUseN, inUseMax atomic.Int64
	stopS := make(chan struct{})
	go func() {
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stopS:
				return
			case <-tk.C:
				v := int64(db.Stats().InUse)
				inUseSum.Add(v)
				inUseN.Add(1)
				if v > inUseMax.Load() {
					inUseMax.Store(v)
				}
			}
		}
	}()
	s0 := db.Stats()
	end := time.Now().Add(dur - 10*time.Second)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w)))
			for time.Now().Before(end) && ctx.Err() == nil {
				t0 := time.Now()
				var err error
				switch variant {
				case "hot-row": // every worker updates the same row: the row lock serialises them, whatever the pool size
					_, err = db.ExecContext(ctx, "UPDATE cap_items SET k = k + 1 WHERE id = 1")
				case "spread-rows": // control: different rows, no lock contention
					_, err = db.ExecContext(ctx, "UPDATE cap_items SET k = k + 1 WHERE id = ?", 1+r.Intn(rows))
				case "tx-hold": // a transaction that holds its connection while the application "does something else"
					var tx *sql.Tx
					if tx, err = db.BeginTx(ctx, nil); err == nil {
						if _, err = tx.ExecContext(ctx, "UPDATE cap_items SET k = k + 1 WHERE id = ?", 1+r.Intn(rows)); err == nil {
							time.Sleep(hold)
							err = tx.Commit()
						} else {
							_ = tx.Rollback()
						}
					}
				default:
					err = fmt.Errorf("unknown variant")
				}
				d := time.Since(t0)
				if err != nil {
					bad.Add(1)
					mu.Lock()
					kinds[errLabel(err)]++
					mu.Unlock()
					continue
				}
				ok.Add(1)
				mu.Lock()
				lat = append(lat, d)
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	close(stopS)
	s1 := db.Stats()
	avgInUse := 0.0
	if inUseN.Load() > 0 {
		avgInUse = float64(inUseSum.Load()) / float64(inUseN.Load())
	}
	secs := (dur - 10*time.Second).Seconds()
	result("mode", "contention", "variant", variant, "workers", workers, "max_open", maxOpen, "hold_ms", ms(hold), "ops", ok.Load(), "ops_per_s", float64(ok.Load())/secs,
		"errors", bad.Load(), "error_kinds", kindsString(kinds), "p50_ms", ms(quantile(lat, 0.5)), "p99_ms", ms(quantile(lat, 0.99)),
		"in_use_avg", avgInUse, "in_use_max", inUseMax.Load(), "wait_count", s1.WaitCount-s0.WaitCount, "wait_total_ms_all_waiters", ms(s1.WaitDuration-s0.WaitDuration))
	return nil
}

// ---- mixed ------------------------------------------------------------------------------------------------------------------

type classStats struct {
	mu       sync.Mutex
	lat      []time.Duration
	ok, errs int64
	kinds    map[string]int
}

func (c *classStats) add(d time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kinds == nil {
		c.kinds = map[string]int{}
	}
	if err != nil {
		c.errs++
		c.kinds[errLabel(err)]++
		return
	}
	c.ok++
	c.lat = append(c.lat, d)
}

func patMixed(ctx context.Context, t target, tlsF string, containers, maxOpen, maxIdle, sites, webRPS, perReq int, webMode string, slowEvery, fastEvery time.Duration, dur time.Duration, rows int) error {
	if err := capSetup(ctx, t, rows); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	queries, weights, err := capQueries("mixed")
	if err != nil {
		return err
	}
	pick := makePicker(queries, weights)
	var dbs []*sql.DB
	var ccs []*countConnector
	for i := 0; i < containers; i++ {
		db, cc, err := newPatDB(t, poolOpts{maxOpen: maxOpen, maxIdle: maxIdle, interpolate: true, tls: tlsF, lifetime: 30 * time.Minute}, nil)
		if err != nil {
			return err
		}
		defer db.Close()
		dbs, ccs = append(dbs, db), append(ccs, cc)
	}
	// one dedicated connection per site that takes GET_LOCK and keeps it (the "one lock per site" design). They are outside the pool.
	var siteConns []*sql.Conn
	siteDB, _, err := newPatDB(t, poolOpts{maxOpen: sites + 2, maxIdle: sites + 2, interpolate: true, tls: tlsF}, nil)
	if err != nil {
		return err
	}
	defer siteDB.Close()
	siteFail := 0
	for i := 0; i < sites; i++ {
		c, err := siteDB.Conn(ctx)
		if err == nil {
			var got int
			err = c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", fmt.Sprintf("site-%d-%d", time.Now().UnixNano(), i)).Scan(&got)
		}
		if err != nil {
			siteFail++
			continue
		}
		siteConns = append(siteConns, c)
	}
	log.Printf("PATTERN-MIXED site lock connections opened=%d failed=%d", len(siteConns), siteFail)

	var slow, fast, web classStats
	end := time.Now().Add(dur - 15*time.Second)
	var wg sync.WaitGroup
	worker := func(db *sql.DB, every time.Duration, st *classStats, offset time.Duration) {
		defer wg.Done()
		time.Sleep(offset)
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		for time.Now().Before(end) && ctx.Err() == nil {
			t0 := time.Now()
			qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := pick(r)(qctx, db, r, rows)
			cancel()
			st.add(time.Since(t0), err)
			time.Sleep(every)
		}
	}
	for i := 0; i < 3; i++ { // slow workers: one query every `slowEvery` (30 s)
		wg.Add(1)
		go worker(dbs[i%containers], slowEvery, &slow, time.Duration(i)*slowEvery/3)
	}
	for i := 0; i < 6; i++ { // fast workers: one query every `fastEvery` (1 s)
		wg.Add(1)
		go worker(dbs[i%containers], fastEvery, &fast, time.Duration(i)*fastEvery/6)
	}
	// web: open-loop requests, each of perReq queries (serial on one connection, or parallel)
	var rr atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		var inflight sync.WaitGroup
		rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
		next := time.Now()
		for time.Now().Before(end) && ctx.Err() == nil {
			next = next.Add(time.Duration(rnd.ExpFloat64() / float64(webRPS) * float64(time.Second)))
			time.Sleep(time.Until(next))
			arrival := next
			db := dbs[int(rr.Add(1))%containers]
			seed := rnd.Int63()
			inflight.Add(1)
			go func() {
				defer inflight.Done()
				r := rand.New(rand.NewSource(seed))
				qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				var err error
				if webMode == "serial" {
					for i := 0; i < perReq && err == nil; i++ {
						err = pick(r)(qctx, db, r, rows)
					}
				} else {
					errc := make(chan error, perReq)
					for i := 0; i < perReq; i++ {
						go func(sd int64) { rr := rand.New(rand.NewSource(sd)); errc <- pick(rr)(qctx, db, rr, rows) }(r.Int63())
					}
					for i := 0; i < perReq; i++ {
						if e := <-errc; e != nil && err == nil {
							err = e
						}
					}
				}
				web.add(time.Since(arrival), err)
			}()
		}
		inflight.Wait()
	}()
	// sample the pools
	var maxInUse, maxOpenSeen atomic.Int64
	stopS := make(chan struct{})
	go func() {
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stopS:
				return
			case <-tk.C:
				var in, op int
				for _, d := range dbs {
					s := d.Stats()
					in, op = in+s.InUse, op+s.OpenConnections
				}
				if int64(in) > maxInUse.Load() {
					maxInUse.Store(int64(in))
				}
				if int64(op) > maxOpenSeen.Load() {
					maxOpenSeen.Store(int64(op))
				}
			}
		}
	}()
	// keep the site connections alive below the Proxy's idle client timeout
	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(end) && ctx.Err() == nil {
			time.Sleep(20 * time.Second)
			for _, c := range siteConns {
				_ = c.PingContext(ctx)
			}
		}
	}()
	wg.Wait()
	close(stopS)
	var waits, idleClosed, lifeClosed int64
	var connects int64
	for i, d := range dbs {
		s := d.Stats()
		waits += s.WaitCount
		idleClosed += s.MaxIdleClosed
		lifeClosed += s.MaxLifetimeClosed
		connects += ccs[i].n.Load()
	}
	for _, c := range siteConns {
		_ = c.Close()
	}
	emit := func(name string, st *classStats) {
		result("mode", "mixed", "class", name, "containers", containers, "max_open", maxOpen, "max_idle", maxIdle, "sites", sites, "web_mode", webMode,
			"ok", st.ok, "errors", st.errs, "error_kinds", kindsString(st.kinds), "p50_ms", ms(quantile(st.lat, 0.5)), "p99_ms", ms(quantile(st.lat, 0.99)))
	}
	emit("web", &web)
	emit("fast", &fast)
	emit("slow", &slow)
	result("mode", "mixed", "class", "pools", "containers", containers, "max_open", maxOpen, "max_idle", maxIdle, "sites", sites, "web_mode", webMode,
		"site_locks_held", len(siteConns), "site_lock_failures", siteFail, "pool_in_use_max", maxInUse.Load(), "pool_open_max", maxOpenSeen.Load(),
		"pool_wait_count", waits, "max_idle_closed", idleClosed, "max_lifetime_closed", lifeClosed, "new_connections_pools", connects)
	return nil
}

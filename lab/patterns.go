package main

// Scenario patterns: small, self-contained probes of how Go's database/sql pool and GORM behave in front of RDS Proxy.
// One binary run = one --mode. Every mode prints PATTERN-RESULT lines (key=value) that scripts/summarize.py tabulates.
//
//	warmup          cold pool vs. pool warmed with parallel PingContext: latency of the first wave of N concurrent queries
//	serial-parallel a request runs K queries on one connection (serial) or on K connections (parallel), open-loop arrivals
//	timeout-cascade per-query context timeout under load: new connections, goodput, queries still running on Aurora
//	gorm-pool       GORM patterns that need a second connection (nested db call in a transaction, hooks, Preload) at small MaxOpenConns
//	pin-patterns    driver/GORM settings that may pin RDS Proxy sessions (interpolateParams, PrepareStmt, SET via DSN, long transaction)
//	stampede        all connections created together reach ConnMaxLifetime together: reconnect spike vs. staggered creation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// countConnector counts (and times) real connection establishments: TCP + TLS + login through the Proxy.
type countConnector struct {
	driver.Connector
	n, ns atomic.Int64
}

func (c *countConnector) Connect(ctx context.Context) (driver.Conn, error) {
	t0 := time.Now()
	cn, err := c.Connector.Connect(ctx)
	c.n.Add(1)
	c.ns.Add(int64(time.Since(t0)))
	return cn, err
}

func newPatDB(t target, o poolOpts, mutate func(*mysql.Config)) (*sql.DB, *countConnector, error) {
	mc := mysql.NewConfig()
	mc.User, mc.Passwd, mc.Net, mc.DBName = t.user, t.pass, "tcp", t.db
	mc.Addr = t.host + ":" + t.port
	if tl := effectiveTLS(o.tls); tl != "" {
		mc.TLSConfig = tl
	}
	mc.InterpolateParams = o.interpolate
	mc.Timeout, mc.ReadTimeout, mc.WriteTimeout = 10*time.Second, 60*time.Second, 60*time.Second
	if mutate != nil {
		mutate(mc)
	}
	base, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, nil, err
	}
	cc := &countConnector{Connector: base}
	db := sql.OpenDB(cc)
	db.SetMaxOpenConns(o.maxOpen)
	db.SetMaxIdleConns(o.maxIdle)
	db.SetConnMaxLifetime(o.lifetime)
	db.SetConnMaxIdleTime(o.idle)
	return db, cc, nil
}

// sideStats records, for the whole run and in every mode: Aurora-side Threads_running / Threads_connected / Questions per second
// (straight from Aurora, bypassing the Proxy) and the load generator's own CPU. One PATTERN-AURORA line at the end.
// NOTE: Questions counts every statement of every session on the instance (the monitoring queries and other sessions included); it is
// NOT the application's QPS. Compare it with the application's own counters (offered / ok), do not substitute one for the other.
type sideStats struct {
	stop, done               chan struct{}
	thrRunMax, thrConnMax, n int
	thrRunSum                int
	q0, q1                   int64
	t0, t1                   time.Time
	cpuMax, cpuSum           float64
	cpuN                     int
}

func startSide() *sideStats {
	s := &sideStats{stop: make(chan struct{}), done: make(chan struct{})}
	db := adminDB()
	go func() {
		defer close(s.done)
		if db != nil {
			defer db.Close()
		}
		pt, pi := cpuTimes()
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tk.C:
			}
			if ct, ci := cpuTimes(); ct > pt {
				c := 100 * (1 - float64(ci-pi)/float64(ct-pt))
				s.cpuSum += c
				s.cpuN++
				if c > s.cpuMax {
					s.cpuMax = c
				}
				pt, pi = ct, ci
			}
			if db == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			rows, err := db.QueryContext(ctx, "SELECT VARIABLE_NAME, VARIABLE_VALUE FROM performance_schema.global_status WHERE VARIABLE_NAME IN ('Threads_running','Threads_connected','Questions')")
			if err == nil {
				var run, conn, q int64
				for rows.Next() {
					var k string
					var v int64
					if rows.Scan(&k, &v) == nil {
						switch k {
						case "Threads_running":
							run = v
						case "Threads_connected":
							conn = v
						case "Questions":
							q = v
						}
					}
				}
				rows.Close()
				s.n++
				s.thrRunSum += int(run)
				if int(run) > s.thrRunMax {
					s.thrRunMax = int(run)
				}
				if int(conn) > s.thrConnMax {
					s.thrConnMax = int(conn)
				}
				if s.q0 == 0 {
					s.q0, s.t0 = q, time.Now()
				}
				s.q1, s.t1 = q, time.Now()
			}
			cancel()
		}
	}()
	return s
}

func (s *sideStats) report() {
	close(s.stop)
	<-s.done
	qps, runAvg, cpuAvg := 0.0, 0.0, 0.0
	if s.t1.After(s.t0) {
		qps = float64(s.q1-s.q0) / s.t1.Sub(s.t0).Seconds()
	}
	if s.n > 0 {
		runAvg = float64(s.thrRunSum) / float64(s.n)
	}
	if s.cpuN > 0 {
		cpuAvg = s.cpuSum / float64(s.cpuN)
	}
	log.Printf("PATTERN-AURORA threads_running_max=%d threads_running_avg=%.2f threads_connected_max=%d db_questions_per_s_all_sessions=%.0f client_cpu_avg=%.1f client_cpu_max=%.1f",
		s.thrRunMax, runAvg, s.thrConnMax, qps, cpuAvg, s.cpuMax)
}

func quantile(d []time.Duration, q float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(q*float64(len(s)-1))]
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// result prints one PATTERN-RESULT line; kv is key, value, key, value...
func result(kv ...any) {
	var sb strings.Builder
	sb.WriteString("PATTERN-RESULT")
	for i := 0; i+1 < len(kv); i += 2 {
		v := kv[i+1]
		if f, ok := v.(float64); ok {
			v = strconv.FormatFloat(f, 'f', 2, 64)
		}
		fmt.Fprintf(&sb, " %v=%v", kv[i], v)
	}
	log.Print(sb.String())
}

func makePicker(queries []capQuery, weights []int) func(r *rand.Rand) capQuery {
	total := 0
	for _, w := range weights {
		total += w
	}
	return func(r *rand.Rand) capQuery {
		x := r.Intn(total)
		for i, w := range weights {
			if x < w {
				return queries[i]
			}
			x -= w
		}
		return queries[0]
	}
}

// adminDB opens a connection straight to the Aurora writer (bypassing the Proxy) for server-side counters.
func adminDB() *sql.DB {
	dh, au := os.Getenv("DB_DIRECT_HOST"), os.Getenv("ADMIN_USER")
	if dh == "" || au == "" {
		return nil
	}
	port := os.Getenv("DB_PORT")
	if port == "" {
		port = "3306"
	}
	db, _, err := newPatDB(target{host: dh, port: port, user: au, pass: os.Getenv("ADMIN_PASS"), db: os.Getenv("DB_NAME")},
		poolOpts{maxOpen: 1, maxIdle: 1, interpolate: true}, nil)
	if err != nil {
		return nil
	}
	return db
}

func globalStatus(ctx context.Context, db *sql.DB, name string) int64 {
	if db == nil {
		return -1
	}
	var k string
	var v int64
	if db.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE '"+name+"'").Scan(&k, &v) != nil {
		return -1
	}
	return v
}

// auroraPoller samples, every 500 ms and straight from Aurora, how many queries of `user` are executing and how many threads exist.
type auroraPoller struct {
	mu                        sync.Mutex
	runMax, runSum, n, thrMax int
	stop                      chan struct{}
	done                      chan struct{}
}

func startAuroraPoller(user string) *auroraPoller {
	p := &auroraPoller{stop: make(chan struct{}), done: make(chan struct{})}
	db := adminDB()
	if db == nil {
		close(p.done)
		return p
	}
	go func() {
		defer close(p.done)
		defer db.Close()
		tk := time.NewTicker(500 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-tk.C:
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var running, threads int
			err := db.QueryRowContext(ctx, "SELECT COALESCE(SUM(command='Query' AND user=?),0), COUNT(*) FROM information_schema.processlist", user).Scan(&running, &threads)
			cancel()
			if err != nil {
				continue
			}
			p.mu.Lock()
			p.n++
			p.runSum += running
			if running > p.runMax {
				p.runMax = running
			}
			if threads > p.thrMax {
				p.thrMax = threads
			}
			p.mu.Unlock()
		}
	}()
	return p
}

func (p *auroraPoller) finish() (runMax int, runAvg float64, thrMax int) {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.n > 0 {
		runAvg = float64(p.runSum) / float64(p.n)
	}
	return p.runMax, runAvg, p.thrMax
}

// openLoop starts requests at Poisson arrivals (rate rps) regardless of how slowly earlier ones finish. Latency is counted from
// the scheduled arrival, so queueing is not hidden. Requests beyond maxInflight are dropped and counted.
type olResult struct {
	lat                                          []time.Duration // SUCCESSFUL requests only (a timed-out request has no latency here)
	lags                                         []time.Duration // scheduled arrival -> the moment the generator issued it (generator delay)
	offered, launched, ok, timeouts, errs, drops int64
	withinSLO                                    int64
	errSample                                    string
}

var patSLO = 100 * time.Millisecond // a request counts as "good" if it succeeded within this time

// olFields is the common block of result fields. p50/p99 are over successful requests only; within_slo_pct is over ALL offered
// requests (dropped, timed out and failed ones count as bad), so a short timeout cannot make the tail look better by failing more.
func olFields(res olResult) []any {
	pct := 0.0
	if res.offered > 0 {
		pct = 100 * float64(res.withinSLO) / float64(res.offered)
	}
	return []any{"offered", res.offered, "launched", res.launched, "dropped", res.drops, "ok", res.ok, "timeouts", res.timeouts, "errors", res.errs,
		"slo_ms", ms(patSLO), "within_slo_pct", pct,
		"p50_success_ms", ms(quantile(res.lat, 0.5)), "p99_success_ms", ms(quantile(res.lat, 0.99)),
		"issue_lag_p99_ms", ms(quantile(res.lags, 0.99)), "issue_lag_max_ms", ms(quantile(res.lags, 1))}
}

func openLoop(ctx context.Context, rps float64, warmup, measure time.Duration, maxInflight int, do func(ctx context.Context, r *rand.Rand) error) olResult {
	var res olResult
	var mu sync.Mutex
	var inflight atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	measureFrom, end := start.Add(warmup), start.Add(warmup+measure)
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	next := start
	// This loop is the ONLY place that decides to launch, so the check below and inflight.Add(1) cannot race. If more than one
	// goroutine ever generates arrivals, replace the counter with a channel semaphore.
	for ctx.Err() == nil {
		next = next.Add(time.Duration(rnd.ExpFloat64() / rps * float64(time.Second)))
		if next.After(end) {
			break
		}
		time.Sleep(time.Until(next))
		arrival := next
		measured := !arrival.Before(measureFrom)
		if measured {
			res.offered++
			res.lags = append(res.lags, time.Since(arrival)) // the generator's own delay; large values mean the load was not what we asked for
		}
		if int(inflight.Load()) >= maxInflight {
			if measured {
				res.drops++
			}
			continue
		}
		if measured {
			res.launched++
		}
		inflight.Add(1)
		wg.Add(1)
		seed := rnd.Int63()
		go func() {
			defer wg.Done()
			err := do(ctx, rand.New(rand.NewSource(seed)))
			d := time.Since(arrival)
			inflight.Add(-1)
			if !measured {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				res.ok++
				res.lat = append(res.lat, d)
				if d <= patSLO {
					res.withinSLO++
				}
			case errors.Is(err, context.DeadlineExceeded):
				res.timeouts++
			default:
				res.errs++
				if res.errSample == "" {
					res.errSample = strings.ReplaceAll(err.Error(), " ", "_")
				}
			}
		}()
	}
	wg.Wait()
	return res
}

func maxInUse(ctx context.Context, db *sql.DB) (stop func() int) {
	var m atomic.Int64
	done := make(chan struct{})
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		tk := time.NewTicker(200 * time.Microsecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				if v := int64(db.Stats().InUse); v > m.Load() {
					m.Store(v)
				}
			}
		}
	}()
	return func() int { close(done); <-fin; return int(m.Load()) }
}

func runPatternsCmd(args []string) error {
	fs := newFlagSet("patterns")
	var cf commonFlags
	cf.register(fs, 10*time.Minute)
	mode := fs.String("mode", "", "warmup | serial-parallel | timeout-cascade | gorm-pool | pin-patterns | stampede | idle-gap | idle")
	tlsF := fs.String("tls", "", "\"\" (follow the infrastructure: skip-verify if the Proxy requires TLS) | none (plain TCP, on purpose) | skip-verify (TLS, certificate not verified)")
	proxyAZ := fs.String("proxy-az", "", "same | cross (PROXY_IP_SAME / PROXY_IP_CROSS)")
	conns := fs.Int("conns", 32, "warmup: concurrent queries / pool size; pin-patterns: workers; stampede: pool size")
	trials := fs.Int("trials", 10, "warmup: number of fresh pools measured")
	pause := fs.Duration("pause", 15*time.Second, "warmup: pause between trials")
	warm := fs.Bool("warm", false, "warmup: PingContext on conns connections in parallel before the first wave")
	variant := fs.String("variant", "", "serial-parallel: serial | parallel. pin-patterns: base | prep-false | gorm-prepare | set-tz | set-sqlmode | loc-only | long-tx. stampede: sync | staggered")
	mix := fs.String("mix", "cpu", "serial-parallel / timeout-cascade: query mix (pk | mixed | cpu)")
	rows := fs.Int("rows", 200000, "rows in cap_items")
	rps := fs.Float64("rps", 30, "serial-parallel: requests per second; timeout-cascade: queries per second")
	perReq := fs.Int("queries-per-request", 5, "serial-parallel: queries per request")
	maxOpen := fs.String("max-open", "20", "pool MaxOpenConns (gorm-pool: comma list)")
	timeout := fs.Duration("timeout", 0, "timeout-cascade: per-query context timeout (0 = none)")
	measure := fs.Duration("measure", 90*time.Second, "serial-parallel / timeout-cascade: measured time after a 15 s warm-up")
	slo := fs.Duration("slo", 100*time.Millisecond, "serial-parallel / timeout-cascade: a request is good if it succeeds within this time (within_slo_pct is over all offered requests)")
	maxInflight := fs.Int("max-inflight", 600, "open-loop: requests in flight above which new arrivals are dropped")
	lifetime := fs.Duration("lifetime", 60*time.Second, "stampede: ConnMaxLifetime")
	think := fs.Duration("think", 50*time.Millisecond, "pin-patterns / stampede: pause between queries per worker")
	gap := fs.Duration("gap", 30*time.Second, "idle-gap: pause between queries of one worker")
	idleTime := fs.Duration("idle-time", 0, "idle-gap: SetConnMaxIdleTime (0 = none)")
	dedicated := fs.Bool("dedicated", false, "idle-gap: every worker owns one db.Conn (idle time of each connection = its own gap)")
	wquery := fs.String("query", "select1", "warmup: select1 | pk (a primary-key lookup on cap_items)")
	_ = fs.Parse(args)

	if *proxyAZ != "" {
		ip := map[string]string{"same": os.Getenv("PROXY_IP_SAME"), "cross": os.Getenv("PROXY_IP_CROSS")}[*proxyAZ]
		if ip == "" {
			return fmt.Errorf("--proxy-az needs PROXY_IP_SAME / PROXY_IP_CROSS (net.env)")
		}
		os.Setenv("DB_HOST", ip)
	}
	patSLO = *slo
	t := targetFromEnv()
	ctx, cancel := runContext(cf.duration)
	defer cancel()
	log.Printf("patterns mode=%s tls=%q proxy_az=%q", *mode, *tlsF, *proxyAZ) // the endpoint is not logged on purpose
	side := startSide()
	defer side.report()
	mo, err := strconv.Atoi(strings.Split(*maxOpen, ",")[0])
	if err != nil || mo < 1 {
		return fmt.Errorf("bad --max-open %q", *maxOpen)
	}
	switch *mode {
	case "warmup":
		if *wquery == "pk" {
			if err := capSetup(ctx, t, *rows); err != nil {
				return fmt.Errorf("setup: %w", err)
			}
		}
		return patWarmup(ctx, t, *tlsF, *conns, *trials, *pause, *warm, *wquery, *rows)
	case "idle-gap":
		return patIdleGap(ctx, t, *tlsF, *conns, *gap, *idleTime, cf.duration, *dedicated)
	case "serial-parallel", "timeout-cascade":
		if err := capSetup(ctx, t, *rows); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		var queries []capQuery
		var weights []int
		if sec, ok := strings.CutPrefix(*mix, "sleep:"); ok { // long server-side query that uses no CPU: does Aurora keep running it after the client gave up?
			d, perr := strconv.ParseFloat(sec, 64)
			if perr != nil || d <= 0 {
				return fmt.Errorf("bad --mix %q (sleep:<seconds>)", *mix)
			}
			queries = []capQuery{func(ctx context.Context, db *sql.DB, _ *rand.Rand, _ int) error {
				var x int
				return db.QueryRowContext(ctx, fmt.Sprintf("SELECT SLEEP(%.3f)", d)).Scan(&x)
			}}
			weights = []int{1}
		} else if queries, weights, err = capQueries(*mix); err != nil {
			return err
		}
		pick := makePicker(queries, weights)
		db, cc, err := newPatDB(t, poolOpts{maxOpen: mo, maxIdle: mo, interpolate: true, tls: *tlsF}, nil)
		if err != nil {
			return err
		}
		defer db.Close()
		if *mode == "serial-parallel" {
			return patSerialParallel(ctx, db, cc, pick, *rows, *variant, *rps, *perReq, *measure, *maxInflight, *mix, mo)
		}
		return patTimeout(ctx, db, cc, pick, *rows, t.user, *rps, *timeout, *measure, *maxInflight, *mix, mo)
	case "gorm-pool":
		return patGorm(ctx, t, *tlsF, *maxOpen)
	case "pin-patterns":
		if err := capSetup(ctx, t, *rows); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		return patPin(ctx, t, *tlsF, *variant, *conns, *think, cf.duration, *rows)
	case "stampede":
		return patStampede(ctx, t, *tlsF, *variant, *conns, *lifetime, *think, cf.duration)
	case "idle":
		// No client at all for the whole run. The Proxy still keeps some connections to Aurora (earlier: "5"); this run records how many
		// the whole Proxy keeps (CloudWatch Sum / Average / Maximum side by side), and how many nodes report (Sum / Average).
		result("mode", "idle", "note", "no_client_connections")
		<-ctx.Done()
		return nil
	}
	return fmt.Errorf("--mode must be warmup | serial-parallel | timeout-cascade | gorm-pool | pin-patterns | stampede | idle-gap | idle")
}

// ---- warmup -------------------------------------------------------------------------------------------------------------

func wave(ctx context.Context, db *sql.DB, n int, kind string, rows int) []time.Duration {
	lat := make([]time.Duration, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			t0 := time.Now()
			var err error
			if kind == "pk" {
				var pad string
				err = db.QueryRowContext(ctx, "SELECT pad FROM cap_items WHERE id = ?", 1+rand.Intn(rows)).Scan(&pad)
			} else {
				var x int
				err = db.QueryRowContext(ctx, "SELECT 1").Scan(&x)
			}
			if err != nil {
				lat[i] = -1
				return
			}
			lat[i] = time.Since(t0)
		}(i)
	}
	close(start)
	wg.Wait()
	var ok []time.Duration
	for _, l := range lat {
		if l >= 0 {
			ok = append(ok, l)
		}
	}
	return ok
}

func patWarmup(ctx context.Context, t target, tlsF string, n, trials int, pause time.Duration, warm bool, kind string, rows int) error {
	var p50s, maxs, warms, w2s []float64
	for i := 0; i < trials && ctx.Err() == nil; i++ {
		db, cc, err := newPatDB(t, poolOpts{maxOpen: n, maxIdle: n, interpolate: true, tls: tlsF}, nil)
		if err != nil {
			return err
		}
		warmMs := 0.0
		if warm {
			t0 := time.Now()
			var wg sync.WaitGroup
			for j := 0; j < n; j++ {
				wg.Add(1)
				go func() { defer wg.Done(); _ = db.PingContext(ctx) }()
			}
			wg.Wait()
			warmMs = ms(time.Since(t0))
		}
		time.Sleep(time.Second) // same idle gap in both variants
		w1 := wave(ctx, db, n, kind, rows)
		c1 := cc.n.Load()
		time.Sleep(100 * time.Millisecond)
		w2 := wave(ctx, db, n, kind, rows)
		p50, mx, p50b := quantile(w1, 0.5), quantile(w1, 1), quantile(w2, 0.5)
		result("mode", "warmup", "trial", i, "warm", warm, "conns", n, "warm_ms", warmMs, "w1_p50_ms", ms(p50), "w1_max_ms", ms(mx),
			"w2_p50_ms", ms(p50b), "connects", c1, "connect_avg_ms", float64(cc.ns.Load())/float64(max(cc.n.Load(), 1))/1e6)
		p50s, maxs, warms, w2s = append(p50s, ms(p50)), append(maxs, ms(mx)), append(warms, warmMs), append(w2s, ms(p50b))
		db.Close()
		time.Sleep(pause)
	}
	med := func(x []float64) float64 {
		d := make([]time.Duration, len(x))
		for i, v := range x {
			d[i] = time.Duration(v * 1e6)
		}
		return ms(quantile(d, 0.5))
	}
	result("mode", "warmup", "summary", 1, "warm", warm, "query", kind, "pause_s", pause.Seconds(), "conns", n, "trials", len(p50s), "warm_ms_med", med(warms),
		"w1_p50_ms_med", med(p50s), "w1_max_ms_med", med(maxs), "w2_p50_ms_med", med(w2s))
	return nil
}

// ---- serial vs parallel -------------------------------------------------------------------------------------------------

func patSerialParallel(ctx context.Context, db *sql.DB, cc *countConnector, pick func(*rand.Rand) capQuery, rows int, variant string, rps float64, k int, measure time.Duration, maxInflight int, mix string, maxOpen int) error {
	if variant != "serial" && variant != "parallel" {
		return fmt.Errorf("--variant must be serial | parallel")
	}
	do := func(ctx context.Context, r *rand.Rand) error {
		if variant == "serial" {
			for i := 0; i < k; i++ {
				if err := pick(r)(ctx, db, r, rows); err != nil {
					return err
				}
			}
			return nil
		}
		errc := make(chan error, k)
		for i := 0; i < k; i++ {
			go func(seed int64) {
				rr := rand.New(rand.NewSource(seed))
				errc <- pick(rr)(ctx, db, rr, rows)
			}(r.Int63())
		}
		var first error
		for i := 0; i < k; i++ {
			if err := <-errc; err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	c0t, c0i := cpuTimes()
	stopU := maxInUse(ctx, db)
	s0 := db.Stats()
	res := openLoop(ctx, rps, 15*time.Second, measure, maxInflight, do)
	inuse := stopU()
	s1 := db.Stats()
	c1t, c1i := cpuTimes()
	cpu := 0.0
	if c1t > c0t {
		cpu = 100 * (1 - float64(c1i-c0i)/float64(c1t-c0t))
	}
	kv := []any{"mode", "serial-parallel", "variant", variant, "rps", rps, "queries_per_request", k, "max_open", maxOpen, "mix", mix}
	kv = append(kv, olFields(res)...)
	kv = append(kv, "wait_count", s1.WaitCount-s0.WaitCount, "wait_total_ms_all_waiters", ms(s1.WaitDuration-s0.WaitDuration), "max_in_use", inuse,
		"connects", cc.n.Load(), "client_cpu", cpu, "err_sample", res.errSample)
	result(kv...)
	return nil
}

// ---- timeout cascade ----------------------------------------------------------------------------------------------------

func patTimeout(ctx context.Context, db *sql.DB, cc *countConnector, pick func(*rand.Rand) capQuery, rows int, user string, rps float64, timeout, measure time.Duration, maxInflight int, mix string, maxOpen int) error {
	do := func(ctx context.Context, r *rand.Rand) error {
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		return pick(r)(ctx, db, r, rows)
	}
	poll := startAuroraPoller(user)
	c0 := cc.n.Load()
	res := openLoop(ctx, rps, 15*time.Second, measure, maxInflight, do)
	runMax, runAvg, thrMax := poll.finish()
	good := float64(res.ok) / measure.Seconds()
	kv := []any{"mode", "timeout-cascade", "timeout_ms", ms(timeout), "rps", rps, "max_open", maxOpen, "mix", mix}
	kv = append(kv, olFields(res)...)
	kv = append(kv, "goodput_qps", good, "new_connections", cc.n.Load()-c0,
		"aurora_running_max", runMax, "aurora_running_avg", runAvg, "aurora_threads_max", thrMax, "err_sample", res.errSample)
	result(kv...)
	return nil
}

// ---- GORM pool ----------------------------------------------------------------------------------------------------------

type PatParent struct {
	ID       uint
	Name     string
	Children []PatChild `gorm:"foreignKey:ParentID"`
	Tags     []PatTag   `gorm:"foreignKey:ParentID"`
}
type PatChild struct {
	ID       uint
	ParentID uint
	Name     string
}
type PatTag struct {
	ID       uint
	ParentID uint
	Label    string
}
type PatHookTx struct {
	ID   uint
	Name string
}
type PatHookDB struct {
	ID   uint
	Name string
}

var patGlobalDB *gorm.DB

// BeforeCreate that uses the transaction it is given: no second connection.
func (h *PatHookTx) BeforeCreate(tx *gorm.DB) error {
	var n int64
	return tx.Session(&gorm.Session{NewDB: true}).Model(&PatTag{}).Count(&n).Error
}

// BeforeCreate that uses the global *gorm.DB (a common mistake): it needs a second connection while the create's transaction holds the first.
func (h *PatHookDB) BeforeCreate(tx *gorm.DB) error {
	var n int64
	return patGlobalDB.WithContext(tx.Statement.Context).Model(&PatTag{}).Count(&n).Error
}

func patGorm(ctx context.Context, t target, tlsF, maxOpenList string) error {
	for _, s := range strings.Split(maxOpenList, ",") {
		mo, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || mo < 1 {
			return fmt.Errorf("bad --max-open %q", maxOpenList)
		}
		sdb, _, err := newPatDB(t, poolOpts{maxOpen: mo, maxIdle: mo, interpolate: true, tls: tlsF}, nil)
		if err != nil {
			return err
		}
		gdb, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sdb, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			return err
		}
		patGlobalDB = gdb
		if mo > 1 { // schema once, with room for the migration's own statements
			if err := gdb.AutoMigrate(&PatParent{}, &PatChild{}, &PatTag{}, &PatHookTx{}, &PatHookDB{}); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
		}
		seed := PatParent{Name: "seed", Children: []PatChild{{Name: "c1"}, {Name: "c2"}}, Tags: []PatTag{{Label: "t1"}}}
		if mo > 1 {
			if err := gdb.Create(&seed).Error; err != nil {
				return fmt.Errorf("seed: %w", err)
			}
		}
		tests := []struct {
			name string
			fn   func(c context.Context) error
		}{
			{"tx-uses-tx", func(c context.Context) error {
				return gdb.WithContext(c).Transaction(func(tx *gorm.DB) error {
					if err := tx.Create(&PatParent{Name: "a"}).Error; err != nil {
						return err
					}
					var n int64
					return tx.Model(&PatTag{}).Count(&n).Error
				})
			}},
			{"tx-uses-db-inside", func(c context.Context) error {
				return gdb.WithContext(c).Transaction(func(tx *gorm.DB) error {
					if err := tx.Create(&PatParent{Name: "b"}).Error; err != nil {
						return err
					}
					var n int64
					return gdb.WithContext(c).Model(&PatTag{}).Count(&n).Error // wrong: a second connection
				})
			}},
			{"preload-two", func(c context.Context) error {
				var ps []PatParent
				return gdb.WithContext(c).Preload("Children").Preload("Tags").Find(&ps).Error
			}},
			{"create-with-associations", func(c context.Context) error {
				return gdb.WithContext(c).Create(&PatParent{Name: "d", Children: []PatChild{{Name: "x"}, {Name: "y"}}, Tags: []PatTag{{Label: "z"}}}).Error
			}},
			{"hook-uses-tx", func(c context.Context) error {
				return gdb.WithContext(c).Create(&PatHookTx{Name: "e"}).Error
			}},
			{"hook-uses-global-db", func(c context.Context) error {
				return gdb.WithContext(c).Create(&PatHookDB{Name: "f"}).Error
			}},
		}
		for _, tc := range tests {
			if ctx.Err() != nil {
				break
			}
			c, cancel := context.WithTimeout(ctx, 3*time.Second)
			stop := maxInUse(c, sdb)
			t0 := time.Now()
			err := tc.fn(c)
			el := time.Since(t0)
			peak := stop()
			cancel()
			res := "ok"
			if err != nil {
				res = "error"
				if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline") {
					res = "blocked"
				}
			}
			es := ""
			if err != nil {
				es = strings.ReplaceAll(err.Error(), " ", "_")
				if len(es) > 80 {
					es = es[:80]
				}
			}
			result("mode", "gorm-pool", "test", tc.name, "max_open", mo, "result", res, "ms", ms(el), "peak_in_use", peak, "err", es)
		}
		sdb.Close()
	}
	return nil
}

// ---- pinning patterns ---------------------------------------------------------------------------------------------------

func patPin(ctx context.Context, t target, tlsF, variant string, workers int, think, dur time.Duration, rows int) error {
	setParams := map[string]map[string]string{ // what the driver sends as `SET k=v` on every new connection
		"set-tz":             {"time_zone": "'+09:00'"},
		"set-sqlmode":        {"sql_mode": "'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION'"},
		"set-select-limit":   {"sql_select_limit": "100000"},
		"set-user-var":       {"@pin_probe": "1"},
		"set-wait-timeout":   {"wait_timeout": "28800"},
		"set-max-exec-time":  {"max_execution_time": "30000"},
		"set-autocommit-off": {"autocommit": "0"},
	}
	known := map[string]bool{"base": true, "prep-false": true, "gorm-prepare": true, "loc-only": true, "long-tx": true}
	if _, ok := setParams[variant]; !ok && !known[variant] {
		return fmt.Errorf("--variant must be one of base prep-false gorm-prepare loc-only long-tx or a set-* variant")
	}
	interp := variant != "prep-false"
	mutate := func(mc *mysql.Config) {
		if p, ok := setParams[variant]; ok {
			mc.Params = p
		}
		switch variant {
		case "loc-only":
			if loc, err := time.LoadLocation("Asia/Tokyo"); err == nil {
				mc.Loc = loc
			}
			mc.ParseTime = true
		}
	}
	sdb, cc, err := newPatDB(t, poolOpts{maxOpen: workers, maxIdle: workers, interpolate: interp, tls: tlsF, lifetime: time.Hour}, mutate)
	if err != nil {
		return err
	}
	defer sdb.Close()
	gdb, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sdb, SkipInitializeWithVersion: true}),
		&gorm.Config{Logger: logger.Discard, PrepareStmt: variant == "gorm-prepare"})
	if err != nil {
		return err
	}
	adm := adminDB()
	if adm != nil {
		defer adm.Close()
	}
	var stopOpen func() int
	{
		var mx atomic.Int64
		done, fin := make(chan struct{}), make(chan struct{})
		go func() { // the Go side's own count of open connections: the Proxy's ClientConnections under-reports
			defer close(fin)
			tk := time.NewTicker(200 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-done:
					return
				case <-tk.C:
					if v := int64(sdb.Stats().OpenConnections); v > mx.Load() {
						mx.Store(v)
					}
				}
			}
		}()
		stopOpen = func() int { close(done); <-fin; return int(mx.Load()) }
	}
	prep0, exec0 := globalStatus(ctx, adm, "Com_stmt_prepare"), globalStatus(ctx, adm, "Com_stmt_execute")
	poll := startAuroraPoller(t.user)
	var queries, errs atomic.Int64
	var wg sync.WaitGroup
	end := time.Now().Add(dur - 20*time.Second)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w)))
			for time.Now().Before(end) && ctx.Err() == nil {
				id := 1 + r.Intn(rows)
				var pad string
				var err error
				if variant == "long-tx" {
					err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
						if err := tx.Raw("SELECT pad FROM cap_items WHERE id = ?", id).Scan(&pad).Error; err != nil {
							return err
						}
						time.Sleep(200 * time.Millisecond) // an external API call inside the transaction
						return tx.Raw("SELECT pad FROM cap_items WHERE id = ?", id+1).Scan(&pad).Error
					})
				} else {
					err = gdb.WithContext(ctx).Raw("SELECT pad FROM cap_items WHERE id = ?", id).Scan(&pad).Error
				}
				if err != nil {
					errs.Add(1)
				} else {
					queries.Add(1)
				}
				time.Sleep(think)
			}
		}(w)
	}
	wg.Wait()
	_, _, thr := poll.finish()
	var tz, sqlMode string // what the server session actually uses (does `loc` change it? does DSN time_zone / sql_mode?)
	_ = sdb.QueryRowContext(context.Background(), "SELECT @@session.time_zone, @@session.sql_mode").Scan(&tz, &sqlMode)
	prep1, exec1 := globalStatus(ctx, adm, "Com_stmt_prepare"), globalStatus(ctx, adm, "Com_stmt_execute")
	result("mode", "pin-patterns", "variant", variant, "workers", workers, "queries", queries.Load(), "errors", errs.Load(),
		"com_stmt_prepare_delta", prep1-prep0, "com_stmt_execute_delta", exec1-exec0, "connects", cc.n.Load(), "aurora_threads_max", thr,
		"go_open_max", stopOpen(), "session_time_zone", strings.ReplaceAll(tz, " ", "_"), "session_sql_mode_len", len(sqlMode))
	return nil
}

// ---- stampede -----------------------------------------------------------------------------------------------------------

func patStampede(ctx context.Context, t target, tlsF, variant string, n int, lifetime, think, dur time.Duration) error {
	if variant != "sync" && variant != "staggered" {
		return fmt.Errorf("--variant must be sync | staggered")
	}
	db, cc, err := newPatDB(t, poolOpts{maxOpen: n, maxIdle: n, interpolate: true, tls: tlsF, lifetime: lifetime}, nil)
	if err != nil {
		return err
	}
	defer db.Close()
	type sample struct {
		at  time.Duration
		lat time.Duration
	}
	var mu sync.Mutex
	var all []sample
	begin := time.Now()
	end := begin.Add(dur - 20*time.Second)
	var wg sync.WaitGroup
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			if variant == "staggered" { // connections are created one after another over one lifetime
				time.Sleep(time.Duration(w) * lifetime / time.Duration(n))
			}
			var local []sample
			for time.Now().Before(end) && ctx.Err() == nil {
				t0 := time.Now()
				var x int
				if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&x); err == nil {
					local = append(local, sample{t0.Sub(begin), time.Since(t0)})
				}
				time.Sleep(think)
			}
			mu.Lock()
			all = append(all, local...)
			mu.Unlock()
		}(w)
	}
	// connects per second
	perSec := map[int]int64{}
	var last int64
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				c := cc.n.Load()
				mu.Lock()
				perSec[int(time.Since(begin).Seconds())] = c - last
				mu.Unlock()
				last = c
			}
		}
	}()
	wg.Wait()
	close(done)
	buckets := map[int][]time.Duration{}
	for _, s := range all {
		b := int(s.at.Seconds())
		buckets[b] = append(buckets[b], s.lat)
	}
	base := lifetime
	if base < 60*time.Second { // lifetime=0 (control: no time-based expiry) uses the same window as the 60 s runs
		base = 60 * time.Second
	}
	steady := int(base.Seconds()) + 5 // both variants are at full load only after one lifetime: compare from here on
	peakConn, peakConnAt := int64(0), 0
	peakP99, peakP99At := time.Duration(0), 0
	var allLat []time.Duration
	var steadyConn int64
	mu.Lock()
	for sec, c := range perSec {
		if sec >= steady {
			steadyConn += c
			if c > peakConn {
				peakConn, peakConnAt = c, sec
			}
		}
	}
	mu.Unlock()
	for sec, ls := range buckets {
		if sec >= steady {
			allLat = append(allLat, ls...)
		}
		if sec >= steady {
			if p := quantile(ls, 0.99); p > peakP99 {
				peakP99, peakP99At = p, sec
			}
		}
	}
	for sec := 0; sec < int(dur.Seconds()); sec++ {
		if ls, ok := buckets[sec]; ok {
			log.Printf("PATTERN-SECOND t=%d connects=%d p50_ms=%.2f p99_ms=%.2f max_ms=%.2f n=%d", sec, perSec[sec], ms(quantile(ls, 0.5)), ms(quantile(ls, 0.99)), ms(quantile(ls, 1)), len(ls))
		}
	}
	// The second with the most reconnects and the second with the worst p99 are not necessarily the same second: report both, with the
	// sample count of each, and the three busiest reconnect seconds.
	type sc struct {
		sec int
		c   int64
	}
	var secs []sc
	mu.Lock()
	for sec, c := range perSec {
		if sec >= steady {
			secs = append(secs, sc{sec, c})
		}
	}
	mu.Unlock()
	sort.Slice(secs, func(i, j int) bool { return secs[i].c > secs[j].c })
	top := []string{}
	for i := 0; i < len(secs) && i < 3; i++ {
		top = append(top, fmt.Sprintf("t%d:c%d/p99=%.0fms/n%d", secs[i].sec, secs[i].c, ms(quantile(buckets[secs[i].sec], 0.99)), len(buckets[secs[i].sec])))
	}
	result("mode", "stampede", "variant", variant, "p99_at_peak_connects_ms", ms(quantile(buckets[peakConnAt], 0.99)), "n_at_peak_connects", len(buckets[peakConnAt]),
		"n_at_peak_p99", len(buckets[peakP99At]), "top3_connect_secs", strings.Join(top, "|"), "conns", n, "lifetime_s", lifetime.Seconds(), "queries", len(all), "total_connects", cc.n.Load(),
		"steady_from_s", steady, "steady_connects", steadyConn, "peak_connects_per_s", peakConn, "peak_connects_at_s", peakConnAt, "peak_p99_ms", ms(peakP99), "peak_p99_at_s", peakP99At,
		"steady_p50_ms", ms(quantile(allLat, 0.5)), "steady_p99_ms", ms(quantile(allLat, 0.99)))
	return nil
}

// ---- idle gap (Proxy IdleClientTimeout vs ConnMaxIdleTime) ------------------------------------------------------------------

// patIdleGap: n workers, each runs one query every `gap`. The Proxy closes client connections idle for IdleClientTimeout (set by the
// experiment's "proxy" block).
//
// Whose idle time is it? With a shared pool and n > 1, a worker can borrow the connection another worker used a moment ago, so the
// physical connection's idle time is NOT `gap`. Therefore:
//   - n == 1 (control): one worker, one connection: the idle time of that connection is exactly gap + query time.
//   - dedicated: every worker owns one db.Conn for the whole run, so each connection's idle time is that worker's own gap (the pool's
//     ConnMaxIdleTime does not apply to a checked-out Conn; this isolates what the Proxy does). A dead connection is NOT retried by
//     database/sql here: the error is counted, then the worker opens a new Conn.
//
// The real start/end times of each query are recorded: the interval between queries is gap + the query's own duration, not gap.
func patIdleGap(ctx context.Context, t target, tlsF string, n int, gap, idleTime, dur time.Duration, dedicated bool) error {
	db, cc, err := newPatDB(t, poolOpts{maxOpen: n, maxIdle: n, interpolate: true, tls: tlsF, idle: idleTime}, nil)
	if err != nil {
		return err
	}
	defer db.Close()
	var mu sync.Mutex
	var lat, idleBefore, startIv []time.Duration
	var queries, errs, appReconnects int64
	errSample := ""
	end := time.Now().Add(dur - 20*time.Second)
	var wg sync.WaitGroup
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			time.Sleep(time.Duration(w) * gap / time.Duration(n)) // spread the workers over one gap
			var conn *sql.Conn
			if dedicated {
				if c, err := db.Conn(ctx); err == nil {
					conn = c
				}
				defer func() {
					if conn != nil {
						conn.Close()
					}
				}()
			}
			var lastStart, lastEnd time.Time
			for time.Now().Before(end) && ctx.Err() == nil {
				t0 := time.Now()
				var x int
				var err error
				if dedicated {
					if conn == nil {
						conn, err = db.Conn(ctx)
					}
					if err == nil {
						err = conn.QueryRowContext(ctx, "SELECT 1").Scan(&x)
					}
				} else {
					err = db.QueryRowContext(ctx, "SELECT 1").Scan(&x)
				}
				t1 := time.Now()
				mu.Lock()
				queries++
				if !lastEnd.IsZero() {
					idleBefore = append(idleBefore, t0.Sub(lastEnd)) // time since this worker's previous query ended
					startIv = append(startIv, t0.Sub(lastStart))
				}
				if err != nil {
					errs++
					if errSample == "" {
						errSample = strings.ReplaceAll(err.Error(), " ", "_")
					}
				} else {
					lat = append(lat, t1.Sub(t0))
				}
				mu.Unlock()
				if err != nil && dedicated && conn != nil { // a dead dedicated connection: replace it, and count that the app had to
					conn.Close()
					conn = nil
					mu.Lock()
					appReconnects++
					mu.Unlock()
				}
				lastStart, lastEnd = t0, t1
				select {
				case <-ctx.Done():
					return
				case <-time.After(gap):
				}
			}
		}(w)
	}
	wg.Wait()
	st := db.Stats()
	p50 := quantile(lat, 0.5)
	slow := 0
	for _, l := range lat {
		if l > p50*3 && l > 10*time.Millisecond {
			slow++
		}
	}
	result("mode", "idle-gap", "gap_s", gap.Seconds(), "idle_time_s", idleTime.Seconds(), "workers", n, "dedicated", dedicated, "queries", queries, "errors", errs,
		"new_connections", cc.n.Load(), "new_conn_per_query", float64(cc.n.Load())/float64(max(queries, 1)), "app_reconnects", appReconnects,
		"actual_idle_before_query_s_p50", quantile(idleBefore, 0.5).Seconds(), "actual_start_interval_s_p50", quantile(startIv, 0.5).Seconds(),
		"p50_success_ms", ms(p50), "p99_success_ms", ms(quantile(lat, 0.99)), "slow_queries", slow, "idle_time_closed", st.MaxIdleTimeClosed, "err_sample", errSample)
	return nil
}

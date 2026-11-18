package main

// Scenario capacity: how much concurrency can the Aurora instance use? Closed-loop load (no think time) at a series of
// concurrency levels N through the Proxy; per level it logs throughput, latency percentiles and the load generator's own
// CPU. Aurora CPU comes from CloudWatch (exp.py collects it); scripts/summarize.py joins both and finds the "knee":
// the smallest N whose QPS is within 10% of the best, and the largest N whose p99 is within 2x of the best p99.
// Throughput that stops growing with N (while p99 grows) means the extra connections only queue inside Aurora.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type capQuery func(ctx context.Context, db *sql.DB, rnd *rand.Rand, rows int) error

func capQueries(mix string) ([]capQuery, []int, error) {
	pk := func(ctx context.Context, db *sql.DB, r *rand.Rand, rows int) error {
		var pad string
		return db.QueryRowContext(ctx, "SELECT pad FROM cap_items WHERE id = ?", 1+r.Intn(rows)).Scan(&pad)
	}
	rng := func(width int) capQuery {
		return func(ctx context.Context, db *sql.DB, r *rand.Rand, rows int) error {
			var n, s int64
			k := r.Intn(1000 - width)
			return db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(k),0) FROM cap_items WHERE k BETWEEN ? AND ?", k, k+width).Scan(&n, &s)
		}
	}
	upd := func(ctx context.Context, db *sql.DB, r *rand.Rand, rows int) error {
		_, err := db.ExecContext(ctx, "UPDATE cap_items SET k = ? WHERE id = ?", r.Intn(1000), 1+r.Intn(rows))
		return err
	}
	switch mix {
	case "pk":
		return []capQuery{pk}, []int{100}, nil
	case "mixed": // 70% point read, 20% ~2000-row range aggregate, 10% update
		return []capQuery{pk, rng(10), upd}, []int{70, 20, 10}, nil
	case "cpu": // ~10000-row range aggregates only: CPU-bound
		return []capQuery{rng(50)}, []int{100}, nil
	}
	return nil, nil, fmt.Errorf("unknown --mix %q (pk | mixed | cpu)", mix)
}

// capFromFile reads the application's own SQL: one statement per line, optionally "weight|SQL". Every "?" is bound to a random
// integer in 1..rows (so write the query against cap_items(id, k, pad) or your own tables whose ids fall in that range).
func capFromFile(path string) ([]capQuery, []int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var qs []capQuery
	var ws []int
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") || strings.HasPrefix(line, "#") {
			continue
		}
		w := 1
		if i := strings.Index(line, "|"); i > 0 {
			if n, err := strconv.Atoi(strings.TrimSpace(line[:i])); err == nil {
				w, line = n, strings.TrimSpace(line[i+1:])
			}
		}
		sqlText, nargs := line, strings.Count(line, "?")
		isRead := strings.HasPrefix(strings.ToLower(sqlText), "select")
		qs = append(qs, func(ctx context.Context, db *sql.DB, r *rand.Rand, rows int) error {
			args := make([]any, nargs)
			for i := range args {
				args[i] = 1 + r.Intn(rows)
			}
			if !isRead {
				_, err := db.ExecContext(ctx, sqlText, args...)
				return err
			}
			rs, err := db.QueryContext(ctx, sqlText, args...)
			if err != nil {
				return err
			}
			defer rs.Close()
			for rs.Next() {
			}
			return rs.Err()
		})
		ws = append(ws, w)
	}
	if len(qs) == 0 {
		return nil, nil, fmt.Errorf("no SQL in %s", path)
	}
	return qs, ws, nil
}

func capSetup(ctx context.Context, t target, rows int) error {
	db, err := newDB(t, poolOpts{maxOpen: 2, maxIdle: 2, interpolate: true})
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS cap_items (id INT PRIMARY KEY, k INT NOT NULL, pad VARCHAR(120) NOT NULL, KEY idx_k (k)) ENGINE=InnoDB"); err != nil {
		return err
	}
	var have int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM cap_items").Scan(&have); err != nil {
		return err
	}
	for from := have + 1; from <= rows; from += 1000 {
		var sb strings.Builder
		sb.WriteString("INSERT INTO cap_items (id, k, pad) VALUES ")
		for i := from; i < from+1000 && i <= rows; i++ {
			if i > from {
				sb.WriteByte(',')
			}
			fmt.Fprintf(&sb, "(%d,%d,'%s')", i, rand.Intn(1000), strings.Repeat("x", 100))
		}
		if _, err := db.ExecContext(ctx, sb.String()); err != nil {
			return err
		}
	}
	log.Printf("CAPACITY-SETUP rows=%d (had %d)", rows, have)
	return nil
}

// cpuTimes returns total and idle jiffies of this machine (/proc/stat), to report the load generator's CPU use.
func cpuTimes() (total, idle uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	f := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i == 3 || i == 4 { // idle, iowait
			idle += n
		}
	}
	return
}

func runCapacityCmd(args []string) error {
	fs := newFlagSet("capacity")
	var cf commonFlags
	cf.register(fs, 60*time.Minute)
	levelsF := fs.String("levels", "1,2,4,8,16,32,64", "concurrency levels (pool MaxOpen = MaxIdle = N, N workers, no think time)")
	levelDur := fs.Duration("level-duration", 2*time.Minute, "measured time per level")
	warmup := fs.Duration("warmup", 20*time.Second, "unmeasured time at the start of each level")
	mix := fs.String("mix", "mixed", "pk | mixed | cpu")
	rows := fs.Int("rows", 200000, "rows in cap_items (created on first use)")
	qfile := fs.String("queries-file", "", "use your own SQL instead of --mix: one statement per line, optional \"weight|SQL\"; each ? gets a random id in 1..rows")
	_ = fs.Parse(args)
	var levels []int
	for _, s := range strings.Split(*levelsF, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 {
			return fmt.Errorf("bad --levels %q", *levelsF)
		}
		levels = append(levels, n)
	}
	var queries []capQuery
	var weights []int
	var err error
	if *qfile != "" {
		queries, weights, err = capFromFile(*qfile)
		*mix = "file:" + *qfile
	} else {
		queries, weights, err = capQueries(*mix)
	}
	if err != nil {
		return err
	}
	totalW := 0
	for _, w := range weights {
		totalW += w
	}
	t := targetFromEnv()
	ctx, cancel := runContext(cf.duration)
	defer cancel()
	if err := capSetup(ctx, t, *rows); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	pick := func(r *rand.Rand) capQuery {
		x := r.Intn(totalW)
		for i, w := range weights {
			if x < w {
				return queries[i]
			}
			x -= w
		}
		return queries[0]
	}
	for _, n := range levels {
		if ctx.Err() != nil {
			break
		}
		db, err := newDB(t, poolOpts{maxOpen: n, maxIdle: n, interpolate: true})
		if err != nil {
			return err
		}
		var mu sync.Mutex
		var lat []int32 // microseconds, measured window only
		var errs int
		measureFrom := time.Now().Add(*warmup)
		end := measureFrom.Add(*levelDur)
		lctx, lcancel := context.WithDeadline(ctx, end)
		var wg sync.WaitGroup
		for w := 0; w < n; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w)))
				var local []int32
				e := 0
				for lctx.Err() == nil {
					t0 := time.Now()
					err := pick(r)(lctx, db, r, *rows)
					if lctx.Err() != nil {
						break
					}
					if t0.Before(measureFrom) {
						continue
					}
					if err != nil {
						e++
						continue
					}
					local = append(local, int32(time.Since(t0)/time.Microsecond))
				}
				mu.Lock()
				lat = append(lat, local...)
				errs += e
				mu.Unlock()
			}(w)
		}
		var c0t, c0i, c1t, c1i uint64
		time.Sleep(time.Until(measureFrom))
		c0t, c0i = cpuTimes()
		wg.Wait()
		c1t, c1i = cpuTimes()
		lcancel()
		db.Close()
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		p := func(q float64) int32 {
			if len(lat) == 0 {
				return 0
			}
			return lat[int(q*float64(len(lat)-1))]
		}
		cpu := 0.0
		if c1t > c0t {
			cpu = 100 * (1 - float64(c1i-c0i)/float64(c1t-c0t))
		}
		log.Printf("CAPACITY-LEVEL n=%d start=%s end=%s queries=%d errors=%d qps=%.1f p50_us=%d p99_us=%d client_cpu=%.0f mix=%s",
			n, measureFrom.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), len(lat), errs,
			float64(len(lat))/levelDur.Seconds(), p(0.5), p(0.99), cpu, *mix)
	}
	return nil
}

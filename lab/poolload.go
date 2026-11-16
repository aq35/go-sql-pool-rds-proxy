package main

// Scenario pool-load: N simulated containers, each with one Go pool and W workers issuing queries.
// Replaces the historical "step 1..6" presets; every former preset is a flag combination:
//
//	old step 1 (plain)            defaults
//	old step 2 (pinning)          --interpolate=false --lifetime=30s --rotate-every=1m --lock-mode=pool
//	old step 4 (fixed)            --idle-time=30s --close-old --bad-conn --lock-mode=pool
//	old step 5 (secret rotation)  step 2 flags + --rotate-secret-every=6m (SECRET=app ./run.sh ...)

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

func runPoolLoad(args []string) error {
	fs := newFlagSet("pool-load")
	var cf commonFlags
	cf.register(fs, 10*time.Minute)
	containers := fs.Int("containers", 3, "number of simulated containers (one pool each)")
	totalConns := fs.Int("total-conns", 4, "MaxOpenConns summed over all containers")
	workers := fs.Int("workers-per-pool", 0, "workers per pool (0: twice the pool's MaxOpenConns)")
	qEvery := fs.Duration("query-every", 500*time.Millisecond, "mean pause between iterations per worker")
	hold := fs.Duration("hold", 0, "run SELECT SLEEP(hold) each iteration so queries overlap and latency is measured (0 = off)")
	maxIdle := fs.Int("max-idle", -1, "SetMaxIdleConns per container (-1 = same as its MaxOpenConns)")
	lifetime := fs.Duration("lifetime", 5*time.Minute, "SetConnMaxLifetime (0 = unlimited)")
	idleTime := fs.Duration("idle-time", 0, "SetConnMaxIdleTime (0 = none)")
	interp := fs.Bool("interpolate", true, "interpolateParams (false => server-side prepared statements, pinned on RDS Proxy)")
	tlsF := fs.String("tls", "", "\"\" (plain) | skip-verify (TLS to the proxy)")
	proxyAZ := fs.String("proxy-az", "", "same | cross: connect to the proxy IP in the client's AZ / the other AZ (PROXY_IP_SAME / PROXY_IP_CROSS)")
	lockMode := fs.String("lock-mode", "none", "none | pool | release | direct | table")
	pinProb := fs.Float64("pin-prob", 0.2, "probability per iteration of taking the lock (lock-mode != none)")
	badConnF := fs.Bool("bad-conn", false, "force-close the connection after the lock (driver.ErrBadConn)")
	rotateEvery := fs.Duration("rotate-every", 5*time.Minute, "replace each container's pool this often (staggered)")
	closeOldF := fs.Bool("close-old", false, "Close() the old pool on rotation (false = leaked, the suspected bug)")
	rotSecretAfter := fs.Duration("rotate-secret-after", 3*time.Minute, "first Secrets Manager rotation (needs --rotate-secret-every)")
	rotSecretEvery := fs.Duration("rotate-secret-every", 0, "Secrets Manager rotate-secret interval (0 = off; needs SECRET=app)")
	lockHolders := fs.Int("lock-holders", 0, "per container: dedicated connections (separate pool) that GET_LOCK once and keep it for the whole run (the 'one lock per worker' design)")
	lockKeepalive := fs.Duration("lock-keepalive", 5*time.Minute, "SELECT 1 on each lock-holder connection this often (must be below the Proxy IdleClientTimeout)")
	_ = fs.Parse(args)

	if *containers < 1 || *totalConns < *containers || cf.duration <= 0 || *workers < 0 || *qEvery <= 0 || cf.reportEvery <= 0 {
		return fmt.Errorf("require total-conns >= containers >= 1, positive duration/query-every/report-every, nonnegative workers-per-pool")
	}
	switch *lockMode {
	case "none", "pool", "release", "direct", "table":
	default:
		return fmt.Errorf("unknown --lock-mode %q", *lockMode)
	}
	if *proxyAZ != "" {
		ip := map[string]string{"same": os.Getenv("PROXY_IP_SAME"), "cross": os.Getenv("PROXY_IP_CROSS")}[*proxyAZ]
		if ip == "" {
			return fmt.Errorf("--proxy-az needs PROXY_IP_SAME / PROXY_IP_CROSS (net.env)")
		}
		os.Setenv("DB_HOST", ip)
	}
	if *hold > 0 {
		holdSQL = fmt.Sprintf("SELECT SLEEP(%.3f)", hold.Seconds())
	}
	po := poolOpts{interpolate: *interp, lifetime: *lifetime, idle: *idleTime, tls: *tlsF}
	b := behaviour{pin: *lockMode != "none", pinProb: *pinProb, badConn: *badConnF, closeOld: *closeOldF, lockMode: *lockMode}
	t := targetFromEnv()
	secretARN := ""
	if *rotSecretEvery > 0 {
		if secretARN = os.Getenv("SECRET_ARN"); secretARN == "" {
			return fmt.Errorf("--rotate-secret-every needs SECRET_ARN (run with SECRET=app ./run.sh ...)")
		}
	}
	ctx, cancel := runContext(cf.duration)
	defer cancel()

	log.Printf("max_idle_per_container=%d hold=%s tls=%q proxy_az=%q", *maxIdle, *hold, *tlsF, *proxyAZ)
	log.Printf("containers=%d total_conns=%d interpolateParams=%v lifetime=%s idleTime=%s rotate=%s closeOld=%v lock=%s badConn=%v target=%s",
		*containers, *totalConns, po.interpolate, po.lifetime, po.idle, *rotateEvery, b.closeOld, b.lockMode, b.badConn, t.host)

	var wg sync.WaitGroup
	start := time.Now()
	if dh := os.Getenv("DB_DIRECT_HOST"); dh != "" { // Proxy-bypassing access: lock-mode=direct and the Aurora-side sampler
		dt := t
		dt.host = dh
		var err error
		if direct, err = newDB(dt, poolOpts{maxOpen: 4, maxIdle: 2, interpolate: true, lifetime: 5 * time.Minute}); err != nil {
			return fmt.Errorf("direct pool: %w", err)
		}
		if au := os.Getenv("ADMIN_USER"); au != "" {
			at := dt
			at.user, at.pass = au, os.Getenv("ADMIN_PASS")
			go sampleAurora(ctx, at, 30*time.Second)
		}
	} else if b.lockMode == "direct" {
		return fmt.Errorf("lock-mode=direct needs DB_DIRECT_HOST (use run.sh)")
	}
	if b.lockMode == "table" {
		pdb, _, err := openPool(t, poolOpts{maxOpen: 1, maxIdle: 1, interpolate: true})
		if err == nil {
			_, err = pdb.Exec("CREATE TABLE IF NOT EXISTS exp_locks (name VARCHAR(64) PRIMARY KEY, expires_at DATETIME NOT NULL)")
			closePool(pdb)
		}
		if err != nil {
			return fmt.Errorf("create exp_locks: %w", err)
		}
	}
	sizes := splitConns(*totalConns, *containers)
	for i := 0; i < *containers; i++ {
		o := po
		o.maxOpen, o.maxIdle = sizes[i], sizes[i]
		if *maxIdle >= 0 {
			o.maxIdle = *maxIdle
		}
		c := &container{id: i, t: t, opts: o, secretARN: secretARN}
		if err := c.rotate(false, 0); err != nil {
			return fmt.Errorf("open pool: %w", err)
		}
		nWorkers := *workers
		if nWorkers == 0 {
			nWorkers = sizes[i] * 2
		}
		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func(w int) { defer wg.Done(); c.worker(ctx, w, b, *qEvery) }(w)
		}
		if *lockHolders > 0 {
			wg.Add(1)
			go func(id int) { defer wg.Done(); holdLocks(ctx, t, id, *lockHolders, *lockKeepalive) }(i)
		}
		wg.Add(1)
		go func() { // staggered pool replacement, like containers restarting / refreshing credentials
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-time.After(*rotateEvery * time.Duration(c.id) / time.Duration(*containers)):
			}
			tk := time.NewTicker(*rotateEvery)
			defer tk.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tk.C:
					if err := c.rotate(b.closeOld, 10*time.Second); err != nil {
						log.Printf("rotate c%d: %v", c.id, err)
					} else {
						log.Printf("rotated pool of container %d (old pool closed=%v)", c.id, b.closeOld)
					}
				}
			}
		}()
	}
	go report(ctx, start, cf.reportEvery)
	if *rotSecretEvery > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-time.After(*rotSecretAfter):
			}
			tk := time.NewTicker(*rotSecretEvery)
			defer tk.Stop()
			for {
				triggerRotation(secretARN)
				select {
				case <-ctx.Done():
					return
				case <-tk.C:
				}
			}
		}()
	}
	wg.Wait()
	log.Printf("done after %s", time.Since(start).Truncate(time.Second))
	return nil
}

// holdLocks models workers that take a site lock at start-up and keep it: n dedicated connections taken from their own
// pool (never closed by lifetime / idle limits), each holding one GET_LOCK, kept alive with SELECT 1. Pinned for the whole run.
func holdLocks(ctx context.Context, t target, container, n int, keepalive time.Duration) {
	db, err := newDB(t, poolOpts{maxOpen: n, maxIdle: n, interpolate: true})
	if err != nil {
		log.Printf("lock-holders c%d: %v", container, err)
		return
	}
	defer db.Close()
	conns := make([]*sql.Conn, 0, n)
	for i := 0; i < n; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			log.Printf("lock-holders c%d conn %d: %v", container, i, err)
			break
		}
		var got sql.NullInt64
		if err := c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 1)", fmt.Sprintf("hold_lock_c%d_%d", container, i)).Scan(&got); err != nil || !got.Valid || got.Int64 != 1 {
			log.Printf("lock-holders c%d lock %d not acquired: %v", container, i, err)
			c.Close()
			break
		}
		conns = append(conns, c)
	}
	log.Printf("LOCK-HOLDERS c%d acquired=%d requested=%d", container, len(conns), n)
	tk := time.NewTicker(keepalive)
	defer tk.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-tk.C:
			for _, c := range conns {
				var one int
				if err := c.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil && ctx.Err() == nil {
					log.Printf("lock-holders c%d keepalive: %v", container, err)
				}
			}
		}
	}
	for _, c := range conns {
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
		_ = c.Close()
	}
}

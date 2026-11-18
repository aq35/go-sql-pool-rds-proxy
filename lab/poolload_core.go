package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"log"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"
)

type container struct {
	id        int
	t         target
	opts      poolOpts
	secretARN string // non-empty => re-read credentials on every pool rotation

	mu  sync.RWMutex
	db  *sql.DB
	gdb *gorm.DB
}

func (c *container) current() (*sql.DB, *gorm.DB) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.db, c.gdb
}

func (c *container) rotate(closeOld bool, grace time.Duration) error {
	if c.secretARN != "" {
		if err := refreshCreds(&c.t, c.secretARN); err != nil {
			log.Printf("c%d refresh creds: %v (keeping old)", c.id, err)
		} else {
			log.Printf("c%d SECRET CURRENT version=%s", c.id, c.t.secretVersion)
		}
	}
	ndb, ngdb, err := openPool(c.t, c.opts)
	if err != nil {
		if c.secretARN != "" {
			log.Printf("c%d SECRET POOL open_failed version=%q", c.id, c.t.secretVersion)
		}
		return err
	}
	c.mu.Lock()
	old := c.db
	c.db, c.gdb = ndb, ngdb
	c.mu.Unlock()
	if c.secretARN != "" {
		log.Printf("c%d SECRET POOL activated version=%q", c.id, c.t.secretVersion)
	}
	if old != nil && closeOld {
		go func() { time.Sleep(grace); closePool(old) }()
	}
	// else: the old pool is simply dropped without Close() (the suspected bug); it stays in allDBs.
	return nil
}

type behaviour struct {
	pin      bool    // run GET_LOCK in some iterations
	pinProb  float64 // probability per iteration
	badConn  bool    // after GET_LOCK, force the connection to be closed (driver.ErrBadConn)
	closeOld bool    // Close() old pool on rotation
	lockMode string  // pool | release | direct | table  (only used when pin is true)
}

// holdSQL (e.g. "SELECT SLEEP(0.05)") makes queries overlap so pool concurrency is real; latency of it is tracked

func (c *container) worker(ctx context.Context, n int, b behaviour, every time.Duration) {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano() + int64(c.id*100+n)))
	for ctx.Err() == nil {
		db, gdb := c.current()
		qctx, cancel := context.WithTimeout(ctx, 20*time.Second)

		var one int
		if err := db.QueryRowContext(qctx, "SELECT 1").Scan(&one); err != nil && ctx.Err() == nil {
			log.Printf("c%d w%d select: %v", c.id, n, err)
		}
		if holdSQL != "" {
			t0 := time.Now()
			if err := db.QueryRowContext(qctx, holdSQL).Scan(&one); err != nil && ctx.Err() == nil {
				log.Printf("c%d w%d hold: %v", c.id, n, err)
			} else {
				recordLat(time.Since(t0))
			}
		}
		// With interpolateParams=false this is a server-side PREPARE/EXECUTE => session pinning on RDS Proxy.
		var res int
		if err := gdb.WithContext(qctx).Raw("SELECT ? + 1", rnd.Intn(100)).Scan(&res).Error; err != nil && ctx.Err() == nil {
			log.Printf("c%d w%d gorm: %v", c.id, n, err)
		}
		if b.pin && rnd.Float64() < b.pinProb {
			doLock(qctx, db, gdb, rnd, b)
		}
		cancel()

		sleep := every/2 + time.Duration(rnd.Int63n(int64(every)))
		select {
		case <-ctx.Done():
		case <-time.After(sleep):
		}
	}
}

// direct is a small pool straight to Aurora (bypassing the Proxy); used by lock-mode=direct and sampling.
var direct *sql.DB

func doLock(ctx context.Context, db *sql.DB, gdb *gorm.DB, rnd *rand.Rand, b behaviour) {
	switch b.lockMode {
	case "pool": // GET_LOCK through the proxy pool, lock never released: connection goes back to the pool pinned
		lockWith(ctx, db, rnd, false, b.badConn)
	case "release": // proper use: RELEASE_LOCK on the same connection (the proxy session stays pinned anyway)
		lockWith(ctx, db, rnd, true, b.badConn)
	case "direct": // lock on a pool that bypasses the proxy
		lockWith(ctx, direct, rnd, true, false)
	case "table": // table based lock, no session state
		lockTable(ctx, gdb, rnd)
	}
}

func lockWith(ctx context.Context, db *sql.DB, rnd *rand.Rand, release, badConn bool) {
	conn, err := db.Conn(ctx)
	if err != nil {
		log.Printf("conn: %v", err)
		return
	}
	defer conn.Close()
	var got sql.NullInt64
	name := "site_lock_" + strconv.FormatInt(rnd.Int63(), 36) // unique: we study pinning, not lock contention
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 1)", name).Scan(&got); err != nil {
		log.Printf("get_lock: %v", err)
		return
	}
	if release {
		_ = conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&got)
	}
	if badConn {
		// Mark the session unusable so database/sql closes the physical connection instead of
		// returning the (possibly pinned) session to the pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}

func lockTable(ctx context.Context, gdb *gorm.DB, rnd *rand.Rand) {
	name := "tl_" + strconv.Itoa(rnd.Intn(8))
	res := gdb.WithContext(ctx).Exec("INSERT INTO exp_locks(name, expires_at) VALUES (?, NOW() + INTERVAL 5 SECOND) "+
		"ON DUPLICATE KEY UPDATE expires_at = IF(expires_at < NOW(), VALUES(expires_at), expires_at)", name)
	if res.Error != nil {
		log.Printf("table lock: %v", res.Error)
		return
	}
	if res.RowsAffected > 0 { // 0 => someone else holds it
		gdb.WithContext(ctx).Exec("DELETE FROM exp_locks WHERE name = ?", name)
	}
}

// sampleAurora logs what Aurora itself sees every interval (Sleep connections, long sleepers, user-level locks).
func sampleAurora(ctx context.Context, t target, every time.Duration) {
	db, err := newDB(t, poolOpts{maxOpen: 1, maxIdle: 1, interpolate: true})
	if err != nil {
		log.Printf("AURORA-SAMPLE disabled: %v", err)
		return
	}
	defer db.Close()
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		var total, sleeping, maxSleep int
		err := db.QueryRowContext(qctx, "SELECT COUNT(*), COALESCE(SUM(command='Sleep'),0), COALESCE(MAX(CASE WHEN command='Sleep' THEN time END),0) FROM information_schema.processlist").Scan(&total, &sleeping, &maxSleep)
		if err != nil {
			log.Printf("AURORA-SAMPLE error: %v", err)
			cancel()
			continue
		}
		locks := "n/a"
		var n int
		if db.QueryRowContext(qctx, "SELECT COUNT(*) FROM performance_schema.metadata_locks WHERE object_type='USER LEVEL LOCK'").Scan(&n) == nil {
			locks = strconv.Itoa(n)
		}
		var name, tc string
		_ = db.QueryRowContext(qctx, "SHOW GLOBAL STATUS LIKE 'Threads_connected'").Scan(&name, &tc)
		cancel()
		log.Printf("AURORA-SAMPLE processlist_total=%d sleep=%d max_sleep_sec=%d user_locks=%s threads_connected=%s (includes this sampler)", total, sleeping, maxSleep, locks, tc)
	}
}

func splitConns(total, n int) []int {
	out := make([]int, n)
	for i := 0; i < total; i++ {
		out[i%n]++
	}
	for i := range out {
		if out[i] == 0 {
			out[i] = 1
		}
	}
	return out
}

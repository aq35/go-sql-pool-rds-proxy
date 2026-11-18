package main

// Scenario rotation: what happens to Go connections when Secrets Manager rotates the DB password behind an RDS Proxy?
//
//	Q1  Do connections that already exist keep working after a rotation?            -> query errors on long-lived connections (--lifetime=0)
//	Q2  Do NEW connections work, with the old (cached) credentials and with refreshed ones?  -> ROT-PROBE lines (stale vs fresh login every few seconds)
//	Q3  If connections are replaced regularly (ConnMaxLifetime) with fresh credentials, how long until no connection of the
//	    old credential version is left? Does pinning (prepared statements, dedicated GET_LOCK connections) stop that?  -> ROT-STATS old_* counters
//
// Every connection is tagged with the credential (secret version) it logged in with and counted until it is closed.
// --cred-mode=static  the pool keeps the password it started with (the usual mistake: a DSN built once at start-up)
// --cred-mode=dynamic each new connection fetches the current credentials (cached --cred-ttl, refreshed after an "Access denied")
// Run with SECRET=app (the rotated appuser secret).

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
)

// ---------------------------------------------------------------- tracked connections

type connReg struct {
	mu                    sync.Mutex
	live                  map[*trackedConn]struct{}
	opened, closed, fails atomic.Int64
}

func newConnReg() *connReg { return &connReg{live: map[*trackedConn]struct{}{}} }

// snapshot: live connections per credential version, and the age of the oldest one.
func (r *connReg) snapshot() (map[string]int, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	by := map[string]int{}
	var oldest time.Duration
	for c := range r.live {
		by[c.ver]++
		if a := time.Since(c.born); a > oldest {
			oldest = a
		}
	}
	return by, oldest
}

// trackedConn wraps a driver.Conn and forwards every optional interface the MySQL driver implements.
type trackedConn struct {
	driver.Conn
	reg  *connReg
	ver  string
	born time.Time
	once sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { c.reg.mu.Lock(); delete(c.reg.live, c); c.reg.mu.Unlock(); c.reg.closed.Add(1) })
	return c.Conn.Close()
}
func (c *trackedConn) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	if x, ok := c.Conn.(driver.QueryerContext); ok {
		return x.QueryContext(ctx, q, a)
	}
	return nil, driver.ErrSkip
}
func (c *trackedConn) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	if x, ok := c.Conn.(driver.ExecerContext); ok {
		return x.ExecContext(ctx, q, a)
	}
	return nil, driver.ErrSkip
}
func (c *trackedConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if x, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return x.PrepareContext(ctx, q)
	}
	return c.Conn.Prepare(q)
}
func (c *trackedConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	if x, ok := c.Conn.(driver.ConnBeginTx); ok {
		return x.BeginTx(ctx, o)
	}
	return c.Conn.Begin() //nolint:staticcheck // the wrapped driver.Conn may only implement the legacy Begin
}
func (c *trackedConn) Ping(ctx context.Context) error {
	if x, ok := c.Conn.(driver.Pinger); ok {
		return x.Ping(ctx)
	}
	return nil
}
func (c *trackedConn) ResetSession(ctx context.Context) error {
	if x, ok := c.Conn.(driver.SessionResetter); ok {
		return x.ResetSession(ctx)
	}
	return nil
}
func (c *trackedConn) IsValid() bool {
	if x, ok := c.Conn.(driver.Validator); ok {
		return x.IsValid()
	}
	return true
}
func (c *trackedConn) CheckNamedValue(nv *driver.NamedValue) error {
	if x, ok := c.Conn.(driver.NamedValueChecker); ok {
		return x.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// ---------------------------------------------------------------- credentials

type credMgr struct {
	mu      sync.Mutex
	t       target
	arn     string
	dynamic bool
	ttl     time.Duration
	last    time.Time
	forced  bool
}

func short(v string) string {
	if len(v) > 6 {
		return v[:6]
	}
	return v
}

func (m *credMgr) get() (user, pass, ver string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dynamic && (m.forced || time.Since(m.last) > m.ttl) {
		if err := refreshCreds(&m.t, m.arn); err != nil {
			log.Printf("ROT-CREDS refresh failed (keeping the previous): %v", err)
		}
		m.last, m.forced = time.Now(), false
	}
	return m.t.user, m.t.pass, short(m.t.secretVersion)
}

func (m *credMgr) force() { m.mu.Lock(); m.forced = true; m.mu.Unlock() }

type rotConnector struct {
	cm   *credMgr
	base *mysql.Config
	reg  *connReg
}

func isAuthErr(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "Access denied") || strings.Contains(err.Error(), "1045"))
}

func (rc *rotConnector) Connect(ctx context.Context) (driver.Conn, error) {
	for attempt := 0; ; attempt++ {
		user, pass, ver := rc.cm.get()
		cfg := rc.base.Clone()
		cfg.User, cfg.Passwd = user, pass
		c, err := mysql.NewConnector(cfg)
		if err == nil {
			var conn driver.Conn
			if conn, err = c.Connect(ctx); err == nil {
				tc := &trackedConn{Conn: conn, reg: rc.reg, ver: ver, born: time.Now()}
				rc.reg.mu.Lock()
				rc.reg.live[tc] = struct{}{}
				rc.reg.mu.Unlock()
				rc.reg.opened.Add(1)
				return tc, nil
			}
		}
		rc.reg.fails.Add(1)
		if rc.cm.dynamic && isAuthErr(err) && attempt == 0 { // the secret changed under us: fetch the new one and retry once
			rc.cm.force()
			continue
		}
		return nil, err
	}
}
func (rc *rotConnector) Driver() driver.Driver { return mysql.MySQLDriver{} }

// ---------------------------------------------------------------- scenario

func errKind(err error) string {
	m := err.Error()
	switch {
	case strings.Contains(m, "Access denied") || strings.Contains(m, "1045"):
		return "auth"
	case strings.Contains(m, "bad connection") || strings.Contains(m, "invalid connection") || strings.Contains(m, "EOF") || strings.Contains(m, "broken pipe") || strings.Contains(m, "reset by peer"):
		return "conn_dead"
	case strings.Contains(m, "timeout") || strings.Contains(m, "deadline"):
		return "timeout"
	}
	return "other"
}

func fmtBy(by map[string]int) string {
	if len(by) == 0 {
		return "-"
	}
	ks := make([]string, 0, len(by))
	for k := range by {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = fmt.Sprintf("%s:%d", k, by[k])
	}
	return strings.Join(parts, "|")
}

func runRotationCmd(args []string) error {
	fs := newFlagSet("rotation")
	var cf commonFlags
	cf.register(fs, 25*time.Minute)
	credMode := fs.String("cred-mode", "dynamic", "static | dynamic (fetch the current credentials for each new connection)")
	credTTL := fs.Duration("cred-ttl", 20*time.Second, "dynamic: how long fetched credentials are reused")
	conns := fs.Int("conns", 12, "MaxOpenConns = MaxIdleConns = workers of the main pool")
	lifetime := fs.Duration("lifetime", 2*time.Minute, "SetConnMaxLifetime (0 = connections are never replaced)")
	qEvery := fs.Duration("query-every", 200*time.Millisecond, "pause between queries per worker")
	prepared := fs.Bool("prepared", false, "workers run server-side prepared statements (interpolateParams=false): pinned on the Proxy")
	lockHolders := fs.Int("lock-holders", 0, "dedicated connections holding one GET_LOCK each for the whole run (never replaced by the pool)")
	rotAfter := fs.Duration("rotate-secret-after", 3*time.Minute, "first Secrets Manager rotation")
	rotEvery := fs.Duration("rotate-secret-every", 6*time.Minute, "interval between rotations (0 = never rotate: control run)")
	probeEvery := fs.Duration("probe-every", 10*time.Second, "log in with stale and with fresh credentials this often")
	_ = fs.Parse(args)
	arn := os.Getenv("SECRET_ARN")
	if arn == "" || os.Getenv("DB_USER") != "appuser" {
		return fmt.Errorf("rotation needs the rotated secret: run with SECRET=app ./run.sh ... (SECRET_ARN / DB_USER=appuser)")
	}
	switch *credMode {
	case "static", "dynamic":
	default:
		return fmt.Errorf("--cred-mode must be static or dynamic")
	}
	t := targetFromEnv()
	if err := refreshCreds(&t, arn); err != nil {
		return fmt.Errorf("initial credentials: %w", err)
	}
	stale := t // the credentials this process started with: used by the "stale" probe (and by static mode)
	cm := &credMgr{t: t, arn: arn, dynamic: *credMode == "dynamic", ttl: *credTTL, last: time.Now()}
	base := mysql.NewConfig()
	base.Net, base.Addr, base.DBName = "tcp", t.host+":"+t.port, t.db
	base.InterpolateParams = !*prepared
	base.Timeout, base.ReadTimeout, base.WriteTimeout = 10*time.Second, 30*time.Second, 30*time.Second
	poolReg, lockReg := newConnReg(), newConnReg()
	mk := func(reg *connReg, n int, life time.Duration) *sql.DB {
		db := sql.OpenDB(&rotConnector{cm: cm, base: base, reg: reg})
		db.SetMaxOpenConns(n)
		db.SetMaxIdleConns(n)
		db.SetConnMaxLifetime(life)
		return db
	}
	ctx, cancel := runContext(cf.duration)
	defer cancel()
	db := mk(poolReg, *conns, *lifetime)
	defer db.Close()
	var curVer atomic.Value
	curVer.Store(short(t.secretVersion))
	var qerr sync.Map // kind -> *atomic.Int64
	var queries atomic.Int64
	countErr := func(err error) {
		v, _ := qerr.LoadOrStore(errKind(err), new(atomic.Int64))
		v.(*atomic.Int64).Add(1)
	}
	var wg sync.WaitGroup
	for w := 0; w < *conns; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			var n int
			for ctx.Err() == nil {
				qctx, c := context.WithTimeout(ctx, 20*time.Second)
				var x int
				var err error
				if *prepared {
					err = db.QueryRowContext(qctx, "SELECT ? + 1", w).Scan(&x)
				} else {
					err = db.QueryRowContext(qctx, "SELECT 1").Scan(&x)
				}
				c()
				if err != nil && ctx.Err() == nil {
					countErr(err)
					if n++; n <= 3 {
						log.Printf("ROT-QUERY-ERR w%d kind=%s: %.120v", w, errKind(err), err)
					}
				} else if err == nil {
					queries.Add(1)
				}
				select {
				case <-ctx.Done():
				case <-time.After(*qEvery):
				}
			}
		}(w)
	}
	if *lockHolders > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ldb := mk(lockReg, *lockHolders, 0)
			defer ldb.Close()
			var held []*sql.Conn
			for i := 0; i < *lockHolders; i++ {
				c, err := ldb.Conn(ctx)
				if err != nil {
					log.Printf("ROT lock-holder conn %d: %v", i, err)
					break
				}
				var got sql.NullInt64
				if err := c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 1)", fmt.Sprintf("rot_lock_%d", i)).Scan(&got); err != nil || got.Int64 != 1 {
					log.Printf("ROT lock-holder %d: lock not acquired: %v", i, err)
					c.Close()
					break
				}
				held = append(held, c)
			}
			log.Printf("ROT-LOCKS acquired=%d requested=%d", len(held), *lockHolders)
			tk := time.NewTicker(30 * time.Second)
			defer tk.Stop()
			for {
				select {
				case <-ctx.Done():
					for _, c := range held {
						_ = c.Raw(func(any) error { return driver.ErrBadConn })
						_ = c.Close()
					}
					return
				case <-tk.C:
					for i, c := range held {
						var x int
						if err := c.QueryRowContext(ctx, "SELECT 1").Scan(&x); err != nil && ctx.Err() == nil {
							countErr(err)
							log.Printf("ROT-LOCK-ERR holder %d kind=%s: %.120v", i, errKind(err), err)
						}
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() { // what is the current credential version? (independent of the pool's own refresh mode)
		defer wg.Done()
		tk := time.NewTicker(10 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				tmp := t
				if err := refreshCreds(&tmp, arn); err == nil && short(tmp.secretVersion) != curVer.Load().(string) {
					curVer.Store(short(tmp.secretVersion))
					log.Printf("ROT-VERSION current=%s", short(tmp.secretVersion))
				}
			}
		}
	}()
	if *rotEvery > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-time.After(*rotAfter):
			}
			tk := time.NewTicker(*rotEvery)
			defer tk.Stop()
			for {
				triggerRotation(arn)
				select {
				case <-ctx.Done():
					return
				case <-tk.C:
				}
			}
		}()
	}
	wg.Add(1)
	go func() { // fresh logins with the credentials this process started with (stale) and with the current ones (fresh)
		defer wg.Done()
		tk := time.NewTicker(*probeEvery)
		defer tk.Stop()
		login := func(tg target) string {
			cfg := base.Clone()
			cfg.User, cfg.Passwd = tg.user, tg.pass
			c, err := mysql.NewConnector(cfg)
			if err != nil {
				return "ERR(" + errKind(err) + ")"
			}
			pctx, pc := context.WithTimeout(ctx, 10*time.Second)
			defer pc()
			conn, err := c.Connect(pctx)
			if err != nil {
				return "ERR(" + errKind(err) + ")"
			}
			conn.Close()
			return "ok"
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
			}
			fresh := t
			if err := refreshCreds(&fresh, arn); err != nil {
				log.Printf("ROT-PROBE refresh: %v", err)
				continue
			}
			log.Printf("ROT-PROBE stale=%s fresh=%s stale_ver=%s fresh_ver=%s", login(stale), login(fresh), short(stale.secretVersion), short(fresh.secretVersion))
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(cf.reportEvery)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
			}
			cur := curVer.Load().(string)
			pby, page := poolReg.snapshot()
			lby, _ := lockReg.snapshot()
			old := func(by map[string]int) int {
				n := 0
				for v, c := range by {
					if v != cur {
						n += c
					}
				}
				return n
			}
			var qe []string
			qerr.Range(func(k, v any) bool { qe = append(qe, fmt.Sprintf("%s:%d", k, v.(*atomic.Int64).Load())); return true })
			sort.Strings(qe)
			if len(qe) == 0 {
				qe = []string{"-"}
			}
			log.Printf("ROT-STATS current=%s pool=%s locks=%s old_pool=%d old_locks=%d pool_oldest_s=%.0f opened=%d closed=%d connect_fail=%d queries=%d query_err=%s",
				cur, fmtBy(pby), fmtBy(lby), old(pby), old(lby), page.Seconds(), poolReg.opened.Load(), poolReg.closed.Load(), poolReg.fails.Load()+lockReg.fails.Load(), queries.Load(), strings.Join(qe, "|"))
		}
	}()
	wg.Wait()
	return nil
}

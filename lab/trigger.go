package main

// 8-trigger: isolate WHAT makes RDS Proxy open / keep backend connections.
// K client sockets are opened, ONE kind of action is applied, then everything just stays idle (sockets open)
// for holdFor, then the client sockets are closed and we keep observing. Compare Proxy DatabaseConnections,
// ...CurrentlySessionPinned, DatabaseConnectionsSetupSucceeded between modes (and against mode "connect").
//
//	connect         login only, no statement                  (does a login alone create a backend connection?)
//	ping            one COM_PING per connection               (does Ping reach the backend?)
//	ping-loop       COM_PING every second per connection
//	select1         one "SELECT 1" per connection             (plain statement, multiplexed)
//	getlock         one GET_LOCK per connection, never released, connection stays open (pinned)
//	getlock-release GET_LOCK then RELEASE_LOCK on the same connection (is pinning undone?)
//	getlock-close   GET_LOCK, then the client socket is closed immediately (does the backend go away?)
//	prepared        one server-side prepared statement (interpolateParams=false)

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"
)

func runTrigger(ctx context.Context, t target, mode string, k int, holdFor, observe time.Duration) error {
	po := poolOpts{maxOpen: k, maxIdle: k, interpolate: mode != "prepared"}
	db, err := newDB(t, po)
	if err != nil {
		return err
	}
	defer db.Close()
	stamp := func(m string) { log.Printf("TRIGGER-EVENT %s mode=%s k=%d", m, mode, k) }

	stamp("connecting")
	conns := make([]*sql.Conn, k)
	for i := range conns {
		c, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("conn %d: %w", i, err)
		}
		conns[i] = c
	}
	stamp("connected")
	time.Sleep(20 * time.Second) // let a login-only effect show up before any statement

	var wg sync.WaitGroup
	stopLoop := make(chan struct{})
	for i, c := range conns {
		name := "trig_lock_" + strconv.Itoa(i)
		var got sql.NullInt64
		var one int
		var err error
		switch mode {
		case "connect":
		case "ping":
			err = c.PingContext(ctx)
		case "ping-loop":
			wg.Add(1)
			go func(c *sql.Conn) {
				defer wg.Done()
				tk := time.NewTicker(time.Second)
				defer tk.Stop()
				for {
					select {
					case <-stopLoop:
						return
					case <-ctx.Done():
						return
					case <-tk.C:
						if e := c.PingContext(ctx); e != nil {
							log.Printf("ping: %v", e)
						}
					}
				}
			}(c)
		case "select1":
			err = c.QueryRowContext(ctx, "SELECT 1").Scan(&one)
		case "getlock", "getlock-release", "getlock-close":
			err = c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 1)", name).Scan(&got)
			if err == nil && mode == "getlock-release" {
				err = c.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&got)
			}
		case "prepared":
			err = c.QueryRowContext(ctx, "SELECT ? + 1", i).Scan(&one)
		default:
			return fmt.Errorf("unknown trigger mode %q", mode)
		}
		if err != nil {
			log.Printf("TRIGGER action failed on conn %d: %v", i, err)
		}
	}
	stamp("action-done")
	if mode == "getlock-close" { // drop the sockets right away: does the Proxy still hold the backend?
		for _, c := range conns {
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
			_ = c.Close()
		}
		conns = nil
		stamp("sockets-closed")
		select {
		case <-ctx.Done():
		case <-time.After(holdFor + observe):
		}
		stamp("end")
		return nil
	}
	select {
	case <-ctx.Done():
	case <-time.After(holdFor):
	}
	close(stopLoop)
	wg.Wait()
	for _, c := range conns {
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
		_ = c.Close()
	}
	stamp("sockets-closed")
	select {
	case <-ctx.Done():
	case <-time.After(observe):
	}
	stamp("end")
	return nil
}

func runTriggerCmd(args []string) error {
	fs := newFlagSet("trigger")
	var cf commonFlags
	cf.register(fs, 12*time.Minute)
	mode := fs.String("trigger-mode", "connect", "connect|ping|ping-loop|select1|getlock|getlock-release|getlock-close|prepared")
	conns := fs.Int("trigger-conns", 10, "client connections")
	holdFor := fs.Duration("trigger-hold", 4*time.Minute, "keep the sockets open (idle) this long after the action")
	observe := fs.Duration("trigger-observe", 5*time.Minute, "keep observing after the sockets are closed")
	_ = fs.Parse(args)
	ctx, cancel := runContext(cf.duration)
	defer cancel()
	return runTrigger(ctx, targetFromEnv(), *mode, *conns, *holdFor, *observe)
}

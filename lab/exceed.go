package main

// 9-exceed: cases where the Proxy's DatabaseConnections may be HIGHER than its ClientConnections.
// Every case logs EXCEED-STATS (live client sockets / opened / closed) so the client-side count can be compared
// with the CloudWatch ClientConnections and DatabaseConnections metrics of the same window.
//
//	churn-select1   control: short-lived connections doing a plain SELECT 1 (multiplexed)
//	churn-getlock   short-lived connections: GET_LOCK, hold the socket `life`, close            (pinned)
//	churn-prepared  same, with a server-side prepared statement (interpolateParams=false)         (pinned)
//	churn-setvar    same, with SET @v = 1 (user variable, untracked by the Proxy)                 (pinned)
//	burst           k connections at once run SELECT SLEEP(life) every 3 min, then close          (not pinned; backends idle afterwards)
//	steady-settz    k long-lived connections, each with a DIFFERENT session time_zone             (separate backend pools?)

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func runExceed(ctx context.Context, t target, mode string, rate float64, life time.Duration, k int) error {
	po := poolOpts{maxOpen: 400, maxIdle: 0, interpolate: mode != "churn-prepared"}
	db, err := newDB(t, po)
	if err != nil {
		return err
	}
	defer db.Close()
	var live, opened, closedN, failed atomic.Int64
	stats := func(tag string) {
		log.Printf("EXCEED-STATS %s mode=%s live=%d opened=%d closed=%d failed=%d", tag, mode, live.Load(), opened.Load(), closedN.Load(), failed.Load())
	}
	go func() {
		tk := time.NewTicker(15 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				stats("tick")
			}
		}
	}()
	drop := func(c *sql.Conn) {
		_ = c.Raw(func(any) error { return driver.ErrBadConn }) // really close the socket
		_ = c.Close()
		live.Add(-1)
		closedN.Add(1)
	}
	sleepCtx := func(d time.Duration) {
		select {
		case <-ctx.Done():
		case <-time.After(d):
		}
	}
	// one short-lived connection: connect, one action, keep the socket open for `life`, close
	cycle := func(n int64) {
		c, err := db.Conn(ctx)
		if err != nil {
			failed.Add(1)
			log.Printf("conn: %v", err)
			return
		}
		live.Add(1)
		opened.Add(1)
		var one int
		var got sql.NullInt64
		switch mode {
		case "churn-select1":
			err = c.QueryRowContext(ctx, "SELECT 1").Scan(&one)
		case "churn-getlock":
			err = c.QueryRowContext(ctx, "SELECT GET_LOCK(?, 1)", fmt.Sprintf("exc_lock_%d", n)).Scan(&got)
		case "churn-prepared":
			err = c.QueryRowContext(ctx, "SELECT ? + 1", n).Scan(&one)
		case "churn-setvar":
			_, err = c.ExecContext(ctx, "SET @v = 1")
		case "burst":
			err = c.QueryRowContext(ctx, fmt.Sprintf("SELECT SLEEP(%f)", life.Seconds())).Scan(&one)
		}
		if err != nil && ctx.Err() == nil {
			failed.Add(1)
			log.Printf("action: %v", err)
		}
		if mode != "burst" {
			sleepCtx(life)
		}
		drop(c)
	}
	var wg sync.WaitGroup
	stats("start")
	switch {
	case strings.HasPrefix(mode, "churn-"):
		if rate <= 0 {
			return fmt.Errorf("exceed-rate must be positive")
		}
		tk := time.NewTicker(time.Duration(float64(time.Second) / rate))
		defer tk.Stop()
		var n int64
	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			case <-tk.C:
				n++
				wg.Add(1)
				go func(n int64) { defer wg.Done(); cycle(n) }(n)
			}
		}
	case mode == "burst":
		for ctx.Err() == nil {
			stats("burst-begin")
			var bw sync.WaitGroup
			for i := 0; i < k; i++ {
				bw.Add(1)
				go func(i int) { defer bw.Done(); cycle(int64(i)) }(i)
			}
			bw.Wait()
			stats("burst-end")
			sleepCtx(3*time.Minute - life)
		}
	case mode == "steady-settz":
		for i := 0; i < k; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				c, err := db.Conn(ctx)
				if err != nil {
					failed.Add(1)
					log.Printf("conn: %v", err)
					return
				}
				live.Add(1)
				opened.Add(1)
				if _, err := c.ExecContext(ctx, fmt.Sprintf("SET time_zone = '+%02d:00'", i%12)); err != nil {
					log.Printf("set tz: %v", err)
				}
				<-ctx.Done()
				drop(c)
			}(i)
		}
	default:
		return fmt.Errorf("unknown exceed mode %q", mode)
	}
	wg.Wait()
	stats("end")
	return nil
}

func runExceedCmd(args []string) error {
	fs := newFlagSet("exceed")
	var cf commonFlags
	cf.register(fs, 10*time.Minute)
	mode := fs.String("exceed-mode", "churn-getlock", "churn-select1|churn-getlock|churn-prepared|churn-setvar|burst|steady-settz")
	rate := fs.Float64("exceed-rate", 0.25, "churn: new connections per second")
	life := fs.Duration("exceed-life", 10*time.Second, "how long each connection stays open (burst: SLEEP time)")
	k := fs.Int("exceed-k", 40, "burst / steady-settz: number of connections")
	_ = fs.Parse(args)
	ctx, cancel := runContext(cf.duration)
	defer cancel()
	return runExceed(ctx, targetFromEnv(), *mode, *rate, *life, *k)
}

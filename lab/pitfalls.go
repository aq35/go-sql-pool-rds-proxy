package main

// patterns --mode=pitfalls: classic ways Go's database/sql costs connections or leaks state, each a small self-checking probe against any
// MySQL (no Proxy, no AWS needed). Every probe prints one PATTERN-RESULT line; scripts/check_pitfalls.py asserts the expected outcome, so CI can
// run it against a MySQL service container.
//
//	rows-not-closed        a *sql.Rows that is neither iterated to the end nor closed keeps its connection (InUse stays up, the pool blocks)
//	rows-fully-read        iterating to the end releases the connection by itself
//	queryrow-without-scan  db.QueryRow(...) whose Row.Scan is never called keeps the connection
//	tx-not-finished        a Begin() without Commit/Rollback keeps the connection for as long as the Tx lives
//	session-state-leak     SET @var / SET time_zone through the pool stays on that connection and shows up in the next, unrelated request
//	ctx-cancel-discards    a query cancelled by its context loses its connection: the next query has to open a new one
//	maxidle-zero           SetMaxIdleConns(0) reconnects for every query; SetMaxIdleConns(2) reuses one connection
//	lifetime-reconnects    SetConnMaxLifetime closes connections that get old; the next query reconnects

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func patPitfalls(ctx context.Context, t target, tlsF string) error {
	// blocked runs `run` with a 500 ms deadline: "ok" if it finishes, "blocked" if it waited for a connection that never came free.
	blocked := func(c context.Context, run func(context.Context) error) string {
		cc, cancel := context.WithTimeout(c, 500*time.Millisecond)
		defer cancel()
		err := run(cc)
		switch {
		case err == nil:
			return "ok"
		case errors.Is(err, context.DeadlineExceeded):
			return "blocked"
		}
		return "error:" + fmt.Sprint(err)
	}

	// ---- rows-not-closed / rows-fully-read -------------------------------------------------------------------------------
	{
		db, _, err := newPatDB(t, poolOpts{maxOpen: 2, maxIdle: 2, interpolate: true, tls: tlsF}, nil)
		if err != nil {
			return err
		}
		r1, err1 := db.QueryContext(ctx, "SELECT 1 UNION SELECT 2")
		r2, err2 := db.QueryContext(ctx, "SELECT 1 UNION SELECT 2")
		if err1 != nil || err2 != nil {
			return fmt.Errorf("rows-not-closed setup: %v %v", err1, err2)
		}
		inUse := db.Stats().InUse
		third := blocked(ctx, func(c context.Context) error {
			var x int
			return db.QueryRowContext(c, "SELECT 1").Scan(&x)
		})
		_ = r1.Close()
		_ = r2.Close()
		after := blocked(ctx, func(c context.Context) error {
			var x int
			return db.QueryRowContext(c, "SELECT 1").Scan(&x)
		})
		result("mode", "pitfalls", "test", "rows-not-closed", "max_open", 2, "in_use_while_open", inUse, "third_query", third, "after_close", after)
		_ = db.Close()
	}
	{
		db, _, err := newPatDB(t, poolOpts{maxOpen: 2, maxIdle: 2, interpolate: true, tls: tlsF}, nil)
		if err != nil {
			return err
		}
		for i := 0; i < 2; i++ {
			rs, err := db.QueryContext(ctx, "SELECT 1 UNION SELECT 2")
			if err != nil {
				return err
			}
			for rs.Next() { // read to the end: database/sql closes the Rows (and returns the connection) itself
			}
		}
		inUse := db.Stats().InUse
		third := blocked(ctx, func(c context.Context) error {
			var x int
			return db.QueryRowContext(c, "SELECT 1").Scan(&x)
		})
		result("mode", "pitfalls", "test", "rows-fully-read", "max_open", 2, "in_use_after_loop", inUse, "third_query", third)
		_ = db.Close()
	}

	// ---- queryrow-without-scan -------------------------------------------------------------------------------------------
	{
		db, _, err := newPatDB(t, poolOpts{maxOpen: 1, maxIdle: 1, interpolate: true, tls: tlsF}, nil)
		if err != nil {
			return err
		}
		row := db.QueryRowContext(ctx, "SELECT 1") // Scan is never called
		_ = row
		inUse := db.Stats().InUse
		second := blocked(ctx, func(c context.Context) error {
			var x int
			return db.QueryRowContext(c, "SELECT 1").Scan(&x)
		})
		result("mode", "pitfalls", "test", "queryrow-without-scan", "max_open", 1, "in_use", inUse, "second_query", second)
		_ = db.Close()
	}

	// ---- tx-not-finished -------------------------------------------------------------------------------------------------
	{
		db, _, err := newPatDB(t, poolOpts{maxOpen: 1, maxIdle: 1, interpolate: true, tls: tlsF}, nil)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		second := blocked(ctx, func(c context.Context) error {
			var x int
			return db.QueryRowContext(c, "SELECT 1").Scan(&x)
		})
		_ = tx.Rollback()
		after := blocked(ctx, func(c context.Context) error {
			var x int
			return db.QueryRowContext(c, "SELECT 1").Scan(&x)
		})
		result("mode", "pitfalls", "test", "tx-not-finished", "max_open", 1, "second_query", second, "after_rollback", after)
		_ = db.Close()
	}

	// ---- session-state-leak ----------------------------------------------------------------------------------------------
	{
		db, _, err := newPatDB(t, poolOpts{maxOpen: 1, maxIdle: 1, interpolate: true, tls: tlsF}, nil)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "SET @pitfall_leak = 42"); err != nil { // a "request" that sets a session variable through the pool
			return err
		}
		if _, err := db.ExecContext(ctx, "SET time_zone = '+09:00'"); err != nil {
			return err
		}
		var v *int
		var tz string
		err1 := db.QueryRowContext(ctx, "SELECT @pitfall_leak").Scan(&v) // an unrelated request, same pooled connection
		err2 := db.QueryRowContext(ctx, "SELECT @@session.time_zone").Scan(&tz)
		leaked := err1 == nil && err2 == nil && v != nil && *v == 42
		seen := -1
		if v != nil {
			seen = *v
		}
		result("mode", "pitfalls", "test", "session-state-leak", "max_open", 1, "user_variable_seen_by_next_request", seen, "time_zone_seen_by_next_request", tz, "leaked", leaked)
		_ = db.Close()
	}

	// ---- ctx-cancel-discards ---------------------------------------------------------------------------------------------
	{
		db, cc, err := newPatDB(t, poolOpts{maxOpen: 1, maxIdle: 1, interpolate: true, tls: tlsF}, nil)
		if err != nil {
			return err
		}
		var x int
		_ = db.QueryRowContext(ctx, "SELECT 1").Scan(&x) // opens the first connection
		before := cc.n.Load()
		cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		errC := db.QueryRowContext(cctx, "SELECT SLEEP(1)").Scan(&x)
		cancel()
		timedOut := errors.Is(errC, context.DeadlineExceeded) || (errC != nil && errC.Error() != "")
		time.Sleep(200 * time.Millisecond)
		errN := db.QueryRowContext(ctx, "SELECT 1").Scan(&x) // the next, normal query
		after := cc.n.Load()
		result("mode", "pitfalls", "test", "ctx-cancel-discards", "cancelled_query_failed", timedOut, "next_query_ok", errN == nil, "connections_before", before, "connections_after", after, "new_connections_after_cancel", after-before)
		_ = db.Close()
	}

	// ---- maxidle-zero ----------------------------------------------------------------------------------------------------
	for _, idle := range []int{0, 2} {
		db, cc, err := newPatDB(t, poolOpts{maxOpen: 4, maxIdle: idle, interpolate: true, tls: tlsF}, nil)
		if err != nil {
			return err
		}
		const n = 20
		for i := 0; i < n; i++ {
			var x int
			_ = db.QueryRowContext(ctx, "SELECT 1").Scan(&x)
		}
		result("mode", "pitfalls", "test", "maxidle-zero", "max_idle", idle, "sequential_queries", n, "connections_opened", cc.n.Load(), "max_idle_closed", db.Stats().MaxIdleClosed)
		_ = db.Close()
	}

	// ---- lifetime-reconnects ---------------------------------------------------------------------------------------------
	{
		db, cc, err := newPatDB(t, poolOpts{maxOpen: 1, maxIdle: 1, interpolate: true, tls: tlsF, lifetime: 600 * time.Millisecond}, nil)
		if err != nil {
			return err
		}
		const n = 10
		for i := 0; i < n; i++ {
			var x int
			_ = db.QueryRowContext(ctx, "SELECT 1").Scan(&x)
			time.Sleep(300 * time.Millisecond)
		}
		result("mode", "pitfalls", "test", "lifetime-reconnects", "lifetime_ms", 600, "queries_every_ms", 300, "queries", n, "connections_opened", cc.n.Load(), "max_lifetime_closed", db.Stats().MaxLifetimeClosed)
		_ = db.Close()
	}
	return nil
}

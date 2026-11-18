package main

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

var (
	_ driver.QueryerContext     = (*trackedConn)(nil)
	_ driver.ExecerContext      = (*trackedConn)(nil)
	_ driver.ConnPrepareContext = (*trackedConn)(nil)
	_ driver.ConnBeginTx        = (*trackedConn)(nil)
	_ driver.Pinger             = (*trackedConn)(nil)
	_ driver.SessionResetter    = (*trackedConn)(nil)
	_ driver.Validator          = (*trackedConn)(nil)
	_ driver.NamedValueChecker  = (*trackedConn)(nil)
)

type fakeConn struct{ closed bool }

func (f *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("x") }
func (f *fakeConn) Close() error                        { f.closed = true; return nil }
func (f *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("x") }

func TestRegistryCountsByVersionAndAge(t *testing.T) {
	r := newConnReg()
	mk := func(ver string, age time.Duration) *trackedConn {
		c := &trackedConn{Conn: &fakeConn{}, reg: r, ver: ver, born: time.Now().Add(-age)}
		r.mu.Lock()
		r.live[c] = struct{}{}
		r.mu.Unlock()
		return c
	}
	a, _ := mk("v1", 5*time.Minute), mk("v1", time.Minute)
	mk("v2", time.Second)
	by, oldest := r.snapshot()
	if by["v1"] != 2 || by["v2"] != 1 || oldest < 5*time.Minute {
		t.Fatalf("by=%v oldest=%v", by, oldest)
	}
	_ = a.Close()
	_ = a.Close() // double close must not double count
	by, _ = r.snapshot()
	if by["v1"] != 1 || r.closed.Load() != 1 {
		t.Fatalf("after close by=%v closed=%d", by, r.closed.Load())
	}
}

func TestErrKindAndFmt(t *testing.T) {
	for in, want := range map[string]string{"Error 1045: Access denied for user": "auth", "driver: bad connection": "conn_dead", "i/o timeout": "timeout", "boom": "other"} {
		if got := errKind(errors.New(in)); got != want {
			t.Fatalf("%q -> %s, want %s", in, got, want)
		}
	}
	if fmtBy(map[string]int{"b": 2, "a": 1}) != "a:1|b:2" || fmtBy(nil) != "-" {
		t.Fatal("fmtBy")
	}
}

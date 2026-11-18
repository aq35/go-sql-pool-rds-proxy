package main

import (
	"context"
	"log"
	"sync/atomic"
	"time"
)

var (
	holdSQL                  string
	latCount, latSum, latMax atomic.Int64
)

var latBuckets [256]atomic.Int64 // 1 ms buckets, last = >=255 ms

func pct(p float64) int {
	var total int64
	for i := range latBuckets {
		total += latBuckets[i].Load()
	}
	if total == 0 {
		return 0
	}
	var acc int64
	for i := range latBuckets {
		acc += latBuckets[i].Load()
		if float64(acc) >= p*float64(total) {
			return i
		}
	}
	return 255
}

func recordLat(d time.Duration) {
	n := d.Nanoseconds()
	b := int(d / time.Millisecond)
	if b > 255 {
		b = 255
	}
	latBuckets[b].Add(1)
	latCount.Add(1)
	latSum.Add(n)
	for {
		m := latMax.Load()
		if n <= m || latMax.CompareAndSwap(m, n) {
			return
		}
	}
}

func avgMs() float64 {
	if n := latCount.Load(); n > 0 {
		return float64(latSum.Load()) / float64(n) / 1e6
	}
	return 0
}

func report(ctx context.Context, start time.Time, every time.Duration) {
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			regMu.Lock()
			var open, inUse, idle, pools, leaked, maxOpen int
			var waits, idleClosed, ltClosed, itClosed int64
			for _, db := range allDBs {
				if closed[db] {
					continue
				}
				s := db.Stats()
				open += s.OpenConnections
				inUse += s.InUse
				idle += s.Idle
				maxOpen += s.MaxOpenConnections
				waits += s.WaitCount
				idleClosed += s.MaxIdleClosed
				ltClosed += s.MaxLifetimeClosed
				itClosed += s.MaxIdleTimeClosed
			}
			pools = len(allDBs)
			leaked = pools - len(closed)
			regMu.Unlock()
			log.Printf("[%s] pools_created=%d pools_not_closed=%d client_side_open=%d (in_use=%d idle=%d) max_open_total=%d wait_count=%d max_idle_closed=%d lifetime_closed=%d idletime_closed=%d hold_queries=%d hold_avg_ms=%.2f hold_max_ms=%.1f hold_p50_ms=%d hold_p99_ms=%d",
				time.Since(start).Truncate(time.Second), pools, leaked, open, inUse, idle, maxOpen, waits, idleClosed, ltClosed, itClosed,
				latCount.Load(), avgMs(), float64(latMax.Load())/1e6, pct(0.5), pct(0.99))
		}
	}
}

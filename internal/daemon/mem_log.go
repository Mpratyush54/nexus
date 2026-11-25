package daemon

// Real-use visibility: a compact memory line in the core log every minute,
// and one line per snapshot cycle (sessions collected, bytes read, time).
// No content and no session ids.

import (
	"fmt"
	"log"
	"runtime/debug"
	"runtime/metrics"
	"sync"
	"sync/atomic"
	"time"
)

// snapStats counts snapshot work since the last cycle line.
var snapStats struct {
	queued    atomic.Int64 // requests waiting for a snapshot worker
	collected atomic.Int64 // transcripts read and packed
	bytesRead atomic.Int64 // their size on disk
}

var memMetricNames = []string{
	"/memory/classes/heap/objects:bytes",  // heap_alloc
	"/memory/classes/heap/unused:bytes",   // inuse = objects + unused
	"/memory/classes/heap/free:bytes",     // idle, not yet returned
	"/memory/classes/heap/released:bytes", // returned to the OS
	"/gc/heap/goal:bytes",                 // next_gc
	"/gc/cycles/total:gc-cycles",
	"/sched/goroutines:goroutines",
	"/memory/classes/total:bytes",
}

type memSnapshot struct {
	alloc, inuse, sys, free, released, nextGC, total uint64
	numGC, goroutines                                uint64
}

func readMemSnapshot() memSnapshot {
	s := make([]metrics.Sample, len(memMetricNames))
	for i, n := range memMetricNames {
		s[i].Name = n
	}
	metrics.Read(s)
	v := func(i int) uint64 {
		if s[i].Value.Kind() == metrics.KindUint64 {
			return s[i].Value.Uint64()
		}
		return 0
	}
	m := memSnapshot{alloc: v(0), free: v(2), released: v(3), nextGC: v(4), numGC: v(5), goroutines: v(6), total: v(7)}
	m.inuse = m.alloc + v(1)
	m.sys = m.inuse + m.free + m.released
	return m
}

func mib(b uint64) string {
	return fmt.Sprintf("%.1fMiB", float64(b)/(1<<20))
}

var memLogOnce sync.Once

// memLogInterval spaces the memory lines.
var memLogInterval = time.Minute

// startMemLog starts the once-per-process memory log.
func startMemLog() {
	memLogOnce.Do(func() {
		go func() {
			t := time.NewTicker(memLogInterval)
			defer t.Stop()
			for range t.C {
				logMemLine("mem")
			}
		}()
	})
}

func logMemLine(prefix string) {
	m := readMemSnapshot()
	log.Printf("%s: heap_alloc=%s heap_inuse=%s heap_sys=%s heap_released=%s next_gc=%s go_total=%s num_gc=%d goroutines=%d snapshot_queue=%d",
		prefix, mib(m.alloc), mib(m.inuse), mib(m.sys), mib(m.released), mib(m.nextGC), mib(m.total),
		m.numGC, m.goroutines, snapStats.queued.Load())
}

// freeOSMemoryAbove: after a snapshot cycle, idle heap not yet returned to
// the OS above this is returned (debug.FreeOSMemory), so private bytes
// follow the live heap. Below it the runtime's scavenger is left alone.
var freeOSMemoryAbove uint64 = 128 << 20

// endSnapshotCycle logs the cycle's work and returns idle heap if large.
func endSnapshotCycle(started time.Time, sessions int) {
	collected := snapStats.collected.Swap(0)
	read := snapStats.bytesRead.Swap(0)
	freed := ""
	if m := readMemSnapshot(); m.free > freeOSMemoryAbove {
		debug.FreeOSMemory()
		freed = " freed_to_os=" + mib(m.free)
	}
	log.Printf("snapshot cycle: sessions=%d collected=%d read=%s took=%s%s",
		sessions, collected, mib(uint64(read)), time.Since(started).Round(time.Millisecond), freed)
}

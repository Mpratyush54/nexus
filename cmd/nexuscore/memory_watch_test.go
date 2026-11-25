//go:build nexuscorelib

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAdaptiveMemoryLimitStaysAboveLiveHeap(t *testing.T) {
	if got := adaptiveMemoryLimit(10 << 20); got != minCoreMemoryLimit {
		t.Fatalf("small heap: %d, want the 384 MiB floor", got)
	}
	live := uint64(262 << 20) // the live heap seen in real use
	if got := adaptiveMemoryLimit(live); got < int64(live)*3/2 {
		t.Fatalf("limit %d MiB for a %d MiB live heap, want at least 1.5x", got>>20, live>>20)
	}
}

var heapSiteSink [][]byte

func TestHeapDiagnosticsWritesProfilesAndSites(t *testing.T) {
	old := runtime.MemProfileRate
	runtime.MemProfileRate = 1
	defer func() { runtime.MemProfileRate = old }()
	for i := 0; i < 64; i++ {
		heapSiteSink = append(heapSiteSink, make([]byte, 64<<10))
	}
	runtime.GC()
	sites := topHeapSites(15, 3)
	found := false
	for _, s := range sites {
		if strings.Contains(s.chain, "TestHeapDiagnosticsWritesProfilesAndSites") && s.bytes >= 4<<20 {
			found = true
		}
	}
	if !found {
		t.Fatalf("test allocation not among the top sites: %+v", sites)
	}
	dir := t.TempDir()
	base := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		writeHeapDiagnostics(dir, base.Add(time.Duration(i)*time.Hour))
	}
	files, _ := filepath.Glob(filepath.Join(dir, "heap-*.pprof"))
	if len(files) != heapProfilesKept || !strings.HasSuffix(files[len(files)-1], "heap-20261125-120000.pprof") {
		t.Fatalf("profiles kept = %v", files)
	}
	if st, err := os.Stat(files[0]); err != nil || st.Size() == 0 {
		t.Fatalf("empty profile: %v", err)
	}
	heapSiteSink = nil
}

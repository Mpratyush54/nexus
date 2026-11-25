//go:build livesmoke

package daemon

// Compares the old per-workspace scanning with the shared scanner on the
// real machine's agent folders. Nothing is uploaded; events are counted.
// Run: go test -tags livesmoke -run TestCaptureCostOnThisMachine -v ./internal/daemon/
// Optional: NEXUS_SMOKE_WORKSPACES="D:\a;D:\b" (defaults below).

import (
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type countingEmitter struct{ n atomic.Int64 }

func (c *countingEmitter) Emit(ev Event) {
	if ev.Type == EventConversationTurn {
		c.n.Add(1)
	}
}

// cpuSeconds is this process's real kernel+user CPU time (Windows).
func cpuSeconds() float64 {
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(syscall.Handle(^uintptr(0)), &creation, &exit, &kernel, &user); err != nil {
		return 0
	}
	ticks := func(f syscall.Filetime) float64 { return float64(uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime)) }
	return (ticks(kernel) + ticks(user)) / 1e7
}

func TestCaptureCostOnThisMachine(t *testing.T) {
	workspaces := []string{`D:\cm-ui`, `D:\central-memory`, `D:\neural_nexus`}
	if env := os.Getenv("NEXUS_SMOKE_WORKSPACES"); env != "" {
		workspaces = strings.Split(env, ";")
	}

	// Old model: one harvester per workspace, each walking every source.
	cpu0, t0 := cpuSeconds(), time.Now()
	oldTurns := int64(0)
	for _, ws := range workspaces {
		c := &countingEmitter{}
		h := NewHarvester(ws, c)
		h.SetSQLiteExtractor("opencode", &openCodeSQLite{Root: ws})
		files, _, _ := h.ScanAndTailCounted()
		oldTurns += c.n.Load()
		t.Logf("old  %-22s files=%d", ws, files)
	}
	oldWall, oldCPU := time.Since(t0), cpuSeconds()-cpu0
	t.Logf("old  one pass for %d workspaces: wall=%s cpu=%.1fs turns=%d (repeated every 30s, cursors lost on restart)", len(workspaces), oldWall.Round(time.Millisecond), oldCPU, oldTurns)

	// New model: one shared scanner.
	state := filepath.Join(t.TempDir(), "cursors.json")
	s := NewSharedScanner(state)
	var harvesters []*Harvester
	var counters []*countingEmitter
	cpu1, t1 := cpuSeconds(), time.Now()
	for _, ws := range workspaces {
		c := &countingEmitter{}
		h := NewHarvester(ws, c)
		h.SetSQLiteExtractor("opencode", &openCodeSQLite{Root: ws})
		harvesters = append(harvesters, h)
		counters = append(counters, c)
		s.Attach(h)
	}
	// Wait for the first reconcile to publish stats on every workspace.
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		ready := true
		for _, h := range harvesters {
			if h.Stats().LastScanAt == "" {
				ready = false
			}
		}
		if ready {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	firstWall, firstCPU := time.Since(t1), cpuSeconds()-cpu1
	newTurns := int64(0)
	for i, h := range harvesters {
		newTurns += counters[i].n.Load()
		t.Logf("new  %-22s files=%d", workspaces[i], h.Stats().LastScanFiles)
	}
	t.Logf("new  first pass (all workspaces together): wall=%s cpu=%.1fs turns=%d", firstWall.Round(time.Millisecond), firstCPU, newTurns)

	// Idle cost: a minute of normal operation with notifications.
	cpu2 := cpuSeconds()
	if prof, err := os.Create(filepath.Join(os.TempDir(), "nexus-idle.pprof")); err == nil {
		_ = pprof.StartCPUProfile(prof)
		defer func() { pprof.StopCPUProfile(); prof.Close() }()
	}
	time.Sleep(60 * time.Second)
	pprof.StopCPUProfile()
	t.Logf("new  idle CPU over 60s: %.2fs", cpuSeconds()-cpu2)
	for _, h := range harvesters {
		s.Detach(h)
	}

	// Restart: cursors persisted, so nothing old is read again.
	again := NewSharedScanner(state)
	c := &countingEmitter{}
	h := NewHarvester(workspaces[0], c)
	cpu3, t3 := cpuSeconds(), time.Now()
	again.Attach(h)
	for h.Stats().LastScanAt == "" && time.Since(t3) < time.Minute {
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("new  after restart: wall=%s cpu=%.2fs re-read turns=%d", time.Since(t3).Round(time.Millisecond), cpuSeconds()-cpu3, c.n.Load())
	again.Detach(h)
}

func TestDiscoverProjectsOnThisMachine(t *testing.T) {
	start, cpu := time.Now(), cpuSeconds()
	got := DiscoverProjects(ResolveSources())
	t.Logf("discovered %d projects in %s (cpu %.1fs)", len(got), time.Since(start).Round(time.Millisecond), cpuSeconds()-cpu)
	for i, p := range got {
		if i == 15 {
			break
		}
		t.Logf("  %-40s sessions=%-4d agents=%v last=%s", p.Path, p.Sessions, p.Agents, p.LastActive.Format("2006-01-02"))
	}
}

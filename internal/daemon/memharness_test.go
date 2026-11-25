//go:build memharness

package daemon

// Memory harness: runs the embedded capture pipeline exactly as nexuscore
// wires it (NewDaemon + NewRuntime + SharedScanner + Runtime.Start, signed in
// to a fake cloud) against a synthetic transcript tree, and samples the Go
// heap every cycle while live sessions keep growing.
//
//	MEMH_DIR=D:/nxsynth MEMH_OUT=D:/nxsynth/out go test -p 1 -tags memharness \
//	    -run TestMemHarness -timeout 60m -v ./internal/daemon
//
// Knobs: MEMH_CYCLES (24), MEMH_CYCLE_SEC (15), MEMH_APPEND_TURNS (8),
// MEMH_IDLE / MEMH_SNAP (Go durations: engine idle timeout and snapshot
// spacing; production defaults when unset), MEMH_LIMIT_MB (256, the core's
// soft GOMEMLIMIT; 0 disables), MEMH_REGEN=1 regenerates the data, MEMH_TAG
// labels output files.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return v
	}
	return def
}

func envDur(key string) time.Duration {
	d, _ := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	return d
}

type fakeCloud struct {
	mu       sync.Mutex
	requests map[string]int
	bytes    map[string]int64
	sessions atomic.Int64
}

func (f *fakeCloud) route(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if len(p) == 36 && strings.Count(p, "-") == 4 {
			parts[i] = "{id}"
		}
		if len(p) == 64 {
			parts[i] = "{hash}"
		}
		if _, err := strconv.Atoi(p); err == nil {
			parts[i] = "{n}"
		}
	}
	return "/" + strings.Join(parts, "/")
}

func (f *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := r.Method + " " + f.route(r.URL.Path)
	// The real server's request caps (internal/server): 2 MB for JSON
	// bodies (decodeJSON), 32 MB on the blob chunk routes. Bodies are
	// counted, not decoded, so the fake adds no heap to the measurement.
	limit := int64(2 << 20)
	if strings.HasSuffix(r.URL.Path, "/chunks") || strings.HasSuffix(r.URL.Path, "/assemble") {
		limit = 32 << 20
	}
	if strings.HasPrefix(r.URL.Path, "/memory/") || strings.HasPrefix(r.URL.Path, "/sessions/") {
		limit = 1 << 62 // legacy routes, not part of this check
	}
	n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, limit+1))
	_, _ = io.Copy(io.Discard, r.Body)
	f.mu.Lock()
	f.requests[route]++
	f.bytes[route] += n
	tooBig := n > limit
	if tooBig {
		f.requests["REJECTED(too large) "+route]++
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if tooBig {
		http.Error(w, `{"error":"invalid JSON body: http: request body too large"}`, http.StatusBadRequest)
		return
	}
	switch {
	case r.URL.Path == "/v1/blobs/missing":
		// Report every blob missing so each publish uploads (the body was
		// drained above, so echo nothing: an absent list means "upload").
		io.WriteString(w, `{}`)
	case strings.HasSuffix(r.URL.Path, "/assemble"):
		io.WriteString(w, `{"chunk_count":1}`)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/agent-sessions":
		id := f.sessions.Add(1)
		fmt.Fprintf(w, `{"id":"00000000-0000-4000-8000-%012d"}`, id%1000)
	case strings.HasSuffix(r.URL.Path, "/versions") && r.Method == http.MethodPost:
		io.WriteString(w, `{"version":1}`)
	case strings.HasPrefix(r.URL.Path, "/memory/search"):
		io.WriteString(w, `{"items":[]}`)
	case r.URL.Path == "/memory/extract":
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"job_id":"j"}`)
	default:
		io.WriteString(w, `{}`)
	}
}

type memSample struct {
	cycle                        int
	elapsed                      float64
	heapLive, heapInuse, heapSys uint64
	totalMem                     uint64 // all memory mapped by the Go runtime
	totalAlloc, mallocs          uint64
	numGC                        uint32
	goroutines                   int
	cpuUser, cpuGC               float64
	appended                     int64
}

var memMetrics = []metrics.Sample{
	{Name: "/gc/heap/live:bytes"},
	{Name: "/memory/classes/total:bytes"},
	{Name: "/cpu/classes/user:cpu-seconds"},
	{Name: "/cpu/classes/gc/total:cpu-seconds"},
}

func readMem(cycle int, start time.Time, appended int64) memSample {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := append([]metrics.Sample(nil), memMetrics...)
	metrics.Read(s)
	return memSample{
		cycle: cycle, elapsed: time.Since(start).Seconds(),
		heapLive: s[0].Value.Uint64(), heapInuse: ms.HeapInuse, heapSys: ms.HeapSys,
		totalMem: s[1].Value.Uint64(), totalAlloc: ms.TotalAlloc, mallocs: ms.Mallocs,
		numGC: ms.NumGC, goroutines: runtime.NumGoroutine(),
		cpuUser: processCPUSeconds(), cpuGC: s[3].Value.Float64(), appended: appended,
	}
}

func mb(b uint64) float64 { return float64(b) / (1 << 20) }

func TestMemHarness(t *testing.T) {
	base := os.Getenv("MEMH_DIR")
	if base == "" {
		t.Skip("set MEMH_DIR to a scratch folder (no spaces)")
	}
	outDir := os.Getenv("MEMH_OUT")
	if outDir == "" {
		outDir = filepath.Join(base, "out")
	}
	tag := os.Getenv("MEMH_TAG")
	if tag == "" {
		tag = "run"
	}
	_ = os.MkdirAll(outDir, 0o755)

	// Data: generate once, then reset live files to their generated size.
	var sessions []synthSession
	manifest := filepath.Join(base, "manifest.json")
	if raw, err := os.ReadFile(manifest); err == nil && os.Getenv("MEMH_REGEN") == "" {
		if err := json.Unmarshal(raw, &sessions); err != nil {
			t.Fatal(err)
		}
	} else {
		_ = os.RemoveAll(filepath.Join(base, "home"))
		_ = os.RemoveAll(filepath.Join(base, "work"))
		t0 := time.Now()
		var err error
		sessions, err = generateSynthetic(base, 42)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("generated %d files in %s", len(sessions), time.Since(t0).Round(time.Second))
	}
	var total int64
	live := 0
	for _, s := range sessions {
		total += s.Bytes
		if s.Live {
			live++
			if err := os.Truncate(s.Path, s.Bytes); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("synthetic tree: %d files, %.0f MB, %d live", len(sessions), float64(total)/(1<<20), live)

	home := filepath.Join(base, "home")
	ws := filepath.Join(base, "work", "demoapp")
	cfg := filepath.Join(base, "config")
	_ = os.RemoveAll(cfg)
	_ = os.RemoveAll(filepath.Join(ws, tokenDirName))
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", filepath.Join(base, "appdata"))
	t.Setenv("LOCALAPPDATA", filepath.Join(base, "localappdata"))
	t.Setenv("CODEX_HOME", "")
	t.Setenv("NEXUS_CONFIG_DIR", cfg)
	t.Setenv("NEXUS_HARVEST_BACKFILL", "")
	t.Setenv("NEXUS_HARVEST_KEEP_TAIL", "")

	if limit := envInt("MEMH_LIMIT_MB", 256); limit > 0 {
		old := debug.SetMemoryLimit(int64(limit) << 20)
		defer debug.SetMemoryLimit(old)
	}

	cloud := &fakeCloud{requests: map[string]int{}, bytes: map[string]int64{}}
	srv := httptest.NewServer(cloud)
	if os.Getenv("MEMH_NOKEEPALIVE") != "" {
		// Count goroutines without idle keep-alive connections.
		srv.Config.SetKeepAlivesEnabled(false)
		if tr, ok := http.DefaultTransport.(*http.Transport); ok {
			tr.DisableKeepAlives = true
			defer func() { tr.DisableKeepAlives = false }()
		}
	}
	defer srv.Close()

	baseGoroutines := runtime.NumGoroutine()
	d, err := NewDaemon(ws, "harness-token")
	if err != nil {
		t.Fatal(err)
	}
	d.ServerURL, d.ServerToken, d.UserID = srv.URL, "cloud-token", "user-1"
	rt := NewRuntime(d, "demoapp", NewStaticDesignation(true))
	shared := NewSharedScanner(filepath.Join(cfg, "capture-cursors.json"))
	rt.Shared = shared
	d.SetPipeline(rt)
	rt.SetServerProjectID("11111111-2222-4333-8444-555555555555")
	if snap := envDur("MEMH_SNAP"); snap > 0 {
		rt.SnapshotMinInterval = snap
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		rt.Start(ctx)
	}()
	if idle := envDur("MEMH_IDLE"); idle > 0 {
		for i := 0; i < 200; i++ {
			shared.mu.Lock()
			e := shared.engine
			shared.mu.Unlock()
			if e != nil {
				e.mu.Lock()
				e.IdleTimeout = idle
				e.mu.Unlock()
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Peak sampler.
	var peakInuse, peakTotal atomic.Uint64
	stopPeak := make(chan struct{})
	go func() {
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/total:bytes"}}
		for {
			select {
			case <-stopPeak:
				return
			case <-tk.C:
				metrics.Read(s)
				if v := s[0].Value.Uint64(); v > peakInuse.Load() {
					peakInuse.Store(v)
				}
				if v := s[1].Value.Uint64(); v > peakTotal.Load() {
					peakTotal.Store(v)
				}
			}
		}
	}()

	cycles := envInt("MEMH_CYCLES", 24)
	cycleEvery := time.Duration(envInt("MEMH_CYCLE_SEC", 15)) * time.Second
	appendTurns := envInt("MEMH_APPEND_TURNS", 8)
	la := newLiveAppender(99, sessions)
	var appended int64
	samples := []memSample{readMem(0, start, 0)}
	for c := 1; c <= cycles; c++ {
		time.Sleep(cycleEvery)
		n, err := la.appendTurns(appendTurns)
		if err != nil {
			t.Fatal(err)
		}
		appended += n
		s := readMem(c, start, appended)
		samples = append(samples, s)
		if c == envInt("MEMH_HEAP_AT", -1) {
			if f, err := os.Create(filepath.Join(outDir, fmt.Sprintf("%s-heap-c%d.pprof", tag, c))); err == nil {
				_ = pprof.Lookup("heap").WriteTo(f, 0)
				f.Close()
			}
		}
		t.Logf("cycle %2d t=%4.0fs live=%6.1fMB inuse=%6.1fMB total=%6.1fMB alloc=%7.0fMB gc=%4d gor=%3d cpu=%5.1fs gc_cpu=%5.1fs",
			c, s.elapsed, mb(s.heapLive), mb(s.heapInuse), mb(s.totalMem), mb(s.totalAlloc), s.numGC, s.goroutines, s.cpuUser, s.cpuGC)
	}
	close(stopPeak)

	// Live chats: how far capture has read each one (they cross 64 MB).
	shared.mu.Lock()
	engine := shared.engine
	shared.mu.Unlock()
	for _, ls := range sessions {
		if !ls.Live || engine == nil {
			continue
		}
		st, _ := os.Stat(ls.Path)
		engine.mu.Lock()
		off := engine.offsets[ls.Path]
		engine.mu.Unlock()
		t.Logf("LIVE %s %s size=%.1fMB read_to=%.1fMB", ls.Agent, filepath.Base(ls.Path), float64(st.Size())/(1<<20), float64(off)/(1<<20))
	}
	t.Logf("SKIPPED %d", len(skippedForHarness()))

	if f, err := os.Create(filepath.Join(outDir, tag+"-heap.pprof")); err == nil {
		_ = pprof.Lookup("heap").WriteTo(f, 0)
		f.Close()
	}
	if f, err := os.Create(filepath.Join(outDir, tag+"-allocs.pprof")); err == nil {
		_ = pprof.Lookup("allocs").WriteTo(f, 0)
		f.Close()
	}
	if f, err := os.Create(filepath.Join(outDir, tag+"-goroutine.txt")); err == nil {
		_ = pprof.Lookup("goroutine").WriteTo(f, 1)
		f.Close()
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Errorf("runtime did not stop within 2m")
	}
	time.Sleep(500 * time.Millisecond)
	after := runtime.NumGoroutine()

	// CSV + summary.
	csv, _ := os.Create(filepath.Join(outDir, tag+"-cycles.csv"))
	fmt.Fprintln(csv, "cycle,elapsed_s,heap_live_mb,heap_inuse_mb,heap_sys_mb,go_total_mb,total_alloc_mb,mallocs,num_gc,goroutines,process_cpu_s,gc_cpu_est_s,appended_mb")
	for _, s := range samples {
		fmt.Fprintf(csv, "%d,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f,%d,%d,%d,%.2f,%.2f,%.1f\n", s.cycle, s.elapsed, mb(s.heapLive), mb(s.heapInuse),
			mb(s.heapSys), mb(s.totalMem), mb(s.totalAlloc), s.mallocs, s.numGC, s.goroutines, s.cpuUser, s.cpuGC, float64(s.appended)/(1<<20))
	}
	csv.Close()
	cloud.mu.Lock()
	var routes []string
	for r, n := range cloud.requests {
		routes = append(routes, fmt.Sprintf("%s n=%d %.1fMB", r, n, float64(cloud.bytes[r])/(1<<20)))
	}
	cloud.mu.Unlock()
	last := samples[len(samples)-1]
	half := samples[len(samples)/2]
	t.Logf("SUMMARY %s: peak_heap_objects=%.1fMB peak_go_total=%.1fMB final_live=%.1fMB alloc_total=%.0fMB alloc_2nd_half=%.0fMB cpu=%.1fs gc_cpu=%.1fs goroutines base=%d run=%d after=%d",
		tag, mb(peakInuse.Load()), mb(peakTotal.Load()), mb(last.heapLive), mb(last.totalAlloc), mb(last.totalAlloc-half.totalAlloc),
		last.cpuUser, last.cpuGC, baseGoroutines, last.goroutines, after)
	for _, r := range routes {
		t.Logf("cloud %s", r)
	}
}

// skippedForHarness lists transcripts capture reports as not read (the
// registry exists from the large-transcript fix on; empty before it).
func skippedForHarness() []SkippedTranscript { return SkippedTranscripts() }

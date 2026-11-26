package continuex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Keep live-session model recording (ACP tests) out of the user's real
// config dir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nexus-model-cache-")
	if err == nil {
		defaultModelCache.dir = func() (string, error) { return dir, nil }
	}
	code := m.Run()
	if err == nil {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}

type fakeModelDiscovery struct {
	calls   atomic.Int32
	mu      sync.Mutex
	catalog HarnessModelCatalog
	err     error
	gate    chan struct{}
}

func (f *fakeModelDiscovery) discover(_ context.Context, agent string, _ bool) (HarnessModelCatalog, error) {
	f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.catalog
	out.Agent = agent
	out.normalize()
	return out, f.err
}

func (f *fakeModelDiscovery) set(catalog HarnessModelCatalog, err error) {
	f.mu.Lock()
	f.catalog, f.err = catalog, err
	f.mu.Unlock()
}

func testModelCache(t *testing.T, fake *fakeModelDiscovery) (*modelCatalogCache, string) {
	dir := t.TempDir()
	cache := newModelCatalogCache(func() (string, error) { return dir, nil }, fake.discover)
	// Runs before the TempDir cleanup: no refresh may still be writing there.
	t.Cleanup(cache.wait)
	return cache, dir
}

func TestModelCacheServesMemoryThenDiskWithoutRespawning(t *testing.T) {
	fake := &fakeModelDiscovery{catalog: HarnessModelCatalog{Source: "fake", Items: []HarnessModel{{ID: "provider/model-a"}, {ID: "provider/model-b"}}}}
	cache, dir := testModelCache(t, fake)
	first, err := cache.get(context.Background(), "opencode", false)
	if err != nil || len(first.Items) != 2 || first.Cached || first.FetchedAt.IsZero() {
		t.Fatalf("first load: %+v %v", first, err)
	}
	second, err := cache.get(context.Background(), "opencode", false)
	if err != nil || !second.Cached || len(second.Items) != 2 || fake.calls.Load() != 1 {
		t.Fatalf("memory hit: %+v %v calls=%d", second, err, fake.calls.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.json")); err != nil {
		t.Fatalf("disk cache missing: %v", err)
	}
	// A new process starts with an empty memory cache and reads the disk.
	restarted := newModelCatalogCache(func() (string, error) { return dir, nil }, fake.discover)
	third, err := restarted.get(context.Background(), "opencode", false)
	if err != nil || !third.Cached || strings.Join(third.Models, ",") != "provider/model-a,provider/model-b" || fake.calls.Load() != 1 {
		t.Fatalf("disk hit: %+v %v calls=%d", third, err, fake.calls.Load())
	}
}

func TestModelCacheReturnsStaleListAndRefreshesInBackground(t *testing.T) {
	fake := &fakeModelDiscovery{catalog: HarnessModelCatalog{Items: []HarnessModel{{ID: "old-model"}}}}
	cache, _ := testModelCache(t, fake)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return base }
	if _, err := cache.get(context.Background(), "codex", false); err != nil {
		t.Fatal(err)
	}
	fake.set(HarnessModelCatalog{Items: []HarnessModel{{ID: "new-model"}}}, nil)
	cache.now = func() time.Time { return base.Add(modelCacheTTL + time.Minute) }
	stale, err := cache.get(context.Background(), "codex", false)
	if err != nil || stale.Models[0] != "old-model" || !stale.Cached {
		t.Fatalf("stale entry should be served instantly: %+v %v", stale, err)
	}
	cache.wait()
	if fake.calls.Load() != 2 {
		t.Fatalf("stale entry should trigger exactly one background refresh, got %d discoveries", fake.calls.Load())
	}
	got, err := cache.get(context.Background(), "codex", false)
	if err != nil || len(got.Models) == 0 || got.Models[0] != "new-model" || !got.Cached {
		t.Fatalf("background refresh did not replace the cached list: %+v %v", got, err)
	}
	cache.wait()
	if fake.calls.Load() != 2 {
		t.Fatalf("a fresh entry must not refresh again, got %d discoveries", fake.calls.Load())
	}
}

func TestModelCacheKeepsLastListWhenRefreshFails(t *testing.T) {
	fake := &fakeModelDiscovery{catalog: HarnessModelCatalog{Items: []HarnessModel{{ID: "kept-model"}}}}
	cache, _ := testModelCache(t, fake)
	if _, err := cache.get(context.Background(), "opencode", false); err != nil {
		t.Fatal(err)
	}
	fake.set(HarnessModelCatalog{}, errors.New("OpenCode model discovery timed out"))
	got, err := cache.get(context.Background(), "opencode", true)
	if err != nil || got.Models[0] != "kept-model" || got.Warning != "" || !got.RefreshFailed {
		t.Fatalf("refresh failure: %+v %v", got, err)
	}
}

func TestModelCacheReportsErrorWithoutAnyList(t *testing.T) {
	fake := &fakeModelDiscovery{err: errors.New("Cursor CLI is not installed")}
	cache, dir := testModelCache(t, fake)
	if _, err := cache.get(context.Background(), "cursor", false); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("expected discovery error, got %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("failed discovery wrote cache files: %v", entries)
	}
}

func TestModelCacheKeepsBuiltInFallbackOutOfDiskCache(t *testing.T) {
	fake := &fakeModelDiscovery{catalog: HarnessModelCatalog{Fallback: true, Warning: "probe timed out", Items: []HarnessModel{{ID: "builtin"}}}}
	cache, dir := testModelCache(t, fake)
	got, err := cache.get(context.Background(), "claude", false)
	if err != nil || !got.Fallback || len(got.Items) == 0 || got.Warning != "" {
		t.Fatalf("fallback: %+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "claude.json")); !os.IsNotExist(err) {
		t.Fatalf("fallback list must not be persisted: %v", err)
	}
	base := time.Now()
	cache.now = func() time.Time { return base.Add(modelFallbackCacheTTL + time.Minute) }
	if _, err := cache.get(context.Background(), "claude", false); err != nil {
		t.Fatal(err)
	}
	cache.wait()
	if fake.calls.Load() != 2 {
		t.Fatalf("fallback should be retried after its short TTL, got %d discoveries", fake.calls.Load())
	}
}

func TestModelCacheSharesOneDiscoveryAcrossConcurrentCallers(t *testing.T) {
	fake := &fakeModelDiscovery{catalog: HarnessModelCatalog{Items: []HarnessModel{{ID: "only-once"}}}, gate: make(chan struct{})}
	cache, _ := testModelCache(t, fake)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := cache.get(context.Background(), "opencode", false); err != nil || got.Models[0] != "only-once" {
				t.Errorf("concurrent get: %+v %v", got, err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(fake.gate)
	wg.Wait()
	if fake.calls.Load() != 1 {
		t.Fatalf("expected one CLI discovery, got %d", fake.calls.Load())
	}
}

func TestModelCacheCallerCancellationDoesNotBlock(t *testing.T) {
	fake := &fakeModelDiscovery{catalog: HarnessModelCatalog{Items: []HarnessModel{{ID: "late-model"}}}, gate: make(chan struct{})}
	cache, _ := testModelCache(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := cache.get(ctx, "opencode", false); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("cancelled caller should return promptly: %v", err)
	}
	close(fake.gate)
	// The discovery outlived its cancelled caller; once it ends the list is cached.
	cache.wait()
	if got, err := cache.get(context.Background(), "opencode", false); err != nil || !got.Cached || got.Models[0] != "late-model" || fake.calls.Load() != 1 {
		t.Fatalf("detached discovery did not fill the cache: %+v %v calls=%d", got, err, fake.calls.Load())
	}
}

func TestModelCacheRecordsLiveSessionModels(t *testing.T) {
	fake := &fakeModelDiscovery{err: errors.New("must not spawn a CLI")}
	cache, _ := testModelCache(t, fake)
	cache.record("kimi", []HarnessModel{{ID: "kimi-k3", Name: "Kimi K3"}, {ID: ""}, {ID: "kimi-k3"}})
	got, err := cache.get(context.Background(), "kimi", false)
	if err != nil || strings.Join(got.Models, ",") != "kimi-k3" || !strings.Contains(got.Source, "live kimi session") || fake.calls.Load() != 0 {
		t.Fatalf("live models: %+v %v calls=%d", got, err, fake.calls.Load())
	}
	cache.record("gemini", nil)
	if _, ok := cache.load("gemini"); ok {
		t.Fatal("an empty advertisement must not create a cache entry")
	}
}

func TestModelCacheRejectsUnsafeAgentPaths(t *testing.T) {
	for _, agent := range []string{"", "../x", `a\b`, "Claude Code"} {
		if validCacheAgent(agent) {
			t.Fatalf("%q accepted", agent)
		}
	}
}

func TestOpenCodeModelParsingSkipsLogNoise(t *testing.T) {
	raw := "\x1b[90mINFO  2026-11-22 loading providers\x1b[0m\nopencode/big-pickle\r\nanthropic/claude-sonnet-5-5\n\nerror: something failed\nnvidia/meta/llama-3.2-11b-vision-instruct\n"
	items := parseOpenCodeModels(raw)
	var ids []string
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if strings.Join(ids, ",") != "opencode/big-pickle,anthropic/claude-sonnet-5-5,nvidia/meta/llama-3.2-11b-vision-instruct" {
		t.Fatalf("opencode ids: %v", ids)
	}
}

func TestClaudeNpmShimResolvesNativeBinary(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("npm .cmd shims are Windows-only")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "claude.cmd")
	native := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	if err := os.MkdirAll(filepath.Dir(native), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{shim, native} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, prefix, err := directExecutable(shim, "claude")
	if err != nil || got != native || len(prefix) != 0 {
		t.Fatalf("claude shim resolved to %q %v %v", got, prefix, err)
	}
}

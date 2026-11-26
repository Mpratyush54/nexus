package continuex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"central-memory/internal/platform"
)

// Model catalogs are cached per harness so opening the picker never waits on
// a CLI: in memory for this process, and on disk under the Nexus config dir
// so a restart shows the last list instantly. A stale entry is still returned
// immediately and refreshed in the background.
const (
	modelCacheTTL         = 24 * time.Hour
	modelFallbackCacheTTL = 5 * time.Minute
	// A failed listing (CLI signed out, offline) is not retried on every
	// picker open or availability check; Refresh still retries at once.
	modelFailureBackoff = 3 * time.Minute
)

type modelFailure struct {
	at  time.Time
	err error
}

type modelCacheEntry struct {
	Catalog   HarnessModelCatalog `json:"catalog"`
	FetchedAt time.Time           `json:"fetched_at"`
}

type modelFlight struct {
	done    chan struct{}
	catalog HarnessModelCatalog
	err     error
}

type modelCatalogCache struct {
	mu       sync.Mutex
	dir      func() (string, error)
	discover func(context.Context, string, bool) (HarnessModelCatalog, error)
	now      func() time.Time
	mem      map[string]modelCacheEntry
	flights  map[string]*modelFlight
	failures map[string]modelFailure
	// defaultModel reads the agent's configured default (cheap file reads),
	// applied on every read so a config change shows without a refresh.
	defaultModel func(agent string) string
	prewarming   atomic.Bool
	// background counts discoveries and refreshes still running after their
	// caller returned (tests wait on it before removing the cache folder).
	background sync.WaitGroup
}

func newModelCatalogCache(dir func() (string, error), discover func(context.Context, string, bool) (HarnessModelCatalog, error)) *modelCatalogCache {
	return &modelCatalogCache{dir: dir, discover: discover, now: time.Now, mem: map[string]modelCacheEntry{}, flights: map[string]*modelFlight{}, failures: map[string]modelFailure{}, defaultModel: func(string) string { return "" }}
}

var defaultModelCache = newModelCatalogCache(func() (string, error) {
	root, err := platform.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "model-catalogs"), nil
}, DiscoverHarnessModels)

func init() {
	defaultModelCache.defaultModel = func(agent string) string {
		home, _ := os.UserHomeDir()
		return configuredDefaultModel(agent, home, os.Getenv)
	}
}

// HarnessModelsCached returns the cached catalog when one exists (refreshing
// it in the background once stale) and otherwise discovers it. refresh forces
// a live discovery; on failure the previous list is kept, quietly.
func HarnessModelsCached(ctx context.Context, agent string, refresh bool) (HarnessModelCatalog, error) {
	return defaultModelCache.get(ctx, agent, refresh)
}

// modelPrewarmAgents have an in-app model picker.
var modelPrewarmAgents = map[string]bool{"claude": true, "codex": true, "opencode": true, "cursor": true, "gemini": true, "kimi": true}

// PrewarmModelCatalogs fills the model cache for every usable agent in the
// background, one at a time, so the first picker open is instant. Fresh
// entries are skipped; a prewarm already running is not restarted.
func PrewarmModelCatalogs(agents []HarnessAvailability) {
	defaultModelCache.prewarm(agents)
}

func (c *modelCatalogCache) prewarm(agents []HarnessAvailability) {
	if !c.prewarming.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.prewarming.Store(false)
		for _, status := range agents {
			agent := normalizeAgent(status.Agent)
			if !status.Usable || !modelPrewarmAgents[agent] {
				continue
			}
			if entry, ok := c.load(agent); ok && c.now().Sub(entry.FetchedAt) <= c.ttl(entry) {
				continue
			}
			if c.recentFailure(agent) != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), modelDiscoveryTimeout+5*time.Second)
			_, _ = c.fetch(ctx, agent, false)
			cancel()
		}
	}()
}

// RecordAdvertisedModels stores the models a live agent session advertised
// (ACP session/new), so the picker reuses them without spawning a CLI.
func RecordAdvertisedModels(agent string, items []HarnessModel) {
	defaultModelCache.record(agent, items)
}

func validCacheAgent(agent string) bool {
	return agent != "" && len(agent) <= 40 && strings.IndexFunc(agent, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) < 0
}

func (c *modelCatalogCache) ttl(entry modelCacheEntry) time.Duration {
	if entry.Catalog.Fallback {
		return modelFallbackCacheTTL
	}
	return modelCacheTTL
}

func (c *modelCatalogCache) get(ctx context.Context, agent string, refresh bool) (HarnessModelCatalog, error) {
	agent = normalizeAgent(agent)
	catalog, err := c.lookup(ctx, agent, refresh)
	if err == nil {
		// A fallback is a normal state, never an excuse line in the picker.
		catalog.Warning = ""
		catalog.applyDefault(c.defaultModel(agent))
	}
	return catalog, err
}

func (c *modelCatalogCache) lookup(ctx context.Context, agent string, refresh bool) (HarnessModelCatalog, error) {
	if !refresh {
		if entry, ok := c.load(agent); ok {
			if c.now().Sub(entry.FetchedAt) > c.ttl(entry) {
				c.refreshInBackground(agent)
			}
			return entry.view(), nil
		}
		if err := c.recentFailure(agent); err != nil {
			return HarnessModelCatalog{Agent: agent}, err
		}
	}
	catalog, err := c.fetch(ctx, agent, refresh)
	if err == nil {
		return catalog, nil
	}
	if entry, ok := c.load(agent); ok {
		view := entry.view()
		view.RefreshFailed = true
		return view, nil
	}
	return catalog, err
}

func (e modelCacheEntry) view() HarnessModelCatalog {
	out := e.Catalog
	out.Items = append([]HarnessModel(nil), out.Items...)
	out.Models = append([]string(nil), out.Models...)
	out.Cached = true
	out.FetchedAt = e.FetchedAt
	return out
}

func (c *modelCatalogCache) refreshInBackground(agent string) {
	c.background.Add(1)
	go func() {
		defer c.background.Done()
		ctx, cancel := context.WithTimeout(context.Background(), modelDiscoveryTimeout+5*time.Second)
		defer cancel()
		_, _ = c.fetch(ctx, agent, true)
	}()
}

// fetch runs at most one discovery per harness at a time; concurrent callers
// share its result. The discovery is detached from the caller's cancellation
// so a closed picker still fills the cache for next time.
func (c *modelCatalogCache) fetch(ctx context.Context, agent string, refresh bool) (HarnessModelCatalog, error) {
	c.mu.Lock()
	flight, running := c.flights[agent]
	if !running {
		flight = &modelFlight{done: make(chan struct{})}
		c.flights[agent] = flight
		c.background.Add(1)
		go func() {
			defer c.background.Done()
			catalog, err := c.discover(context.WithoutCancel(ctx), agent, refresh)
			if err == nil && len(catalog.Items) > 0 {
				catalog.FetchedAt = c.now()
				c.store(agent, modelCacheEntry{Catalog: catalog, FetchedAt: catalog.FetchedAt})
			}
			c.mu.Lock()
			if err != nil {
				c.failures[agent] = modelFailure{at: c.now(), err: err}
			} else {
				delete(c.failures, agent)
			}
			c.mu.Unlock()
			flight.catalog, flight.err = catalog, err
			c.mu.Lock()
			delete(c.flights, agent)
			c.mu.Unlock()
			close(flight.done)
		}()
	}
	c.mu.Unlock()
	select {
	case <-flight.done:
		return flight.catalog, flight.err
	case <-ctx.Done():
		return HarnessModelCatalog{Agent: agent}, errors.New("model loading was cancelled")
	}
}

// wait blocks until every background discovery and refresh has finished
// (its result stored in memory and on disk).
func (c *modelCatalogCache) wait() { c.background.Wait() }

func (c *modelCatalogCache) recentFailure(agent string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f, ok := c.failures[agent]; ok && c.now().Sub(f.at) < modelFailureBackoff {
		return f.err
	}
	return nil
}

func (c *modelCatalogCache) record(agent string, items []HarnessModel) {
	agent = normalizeAgent(agent)
	catalog := HarnessModelCatalog{Agent: agent, Source: "Models advertised by a live " + agent + " session", Items: items}
	catalog.normalize()
	if len(catalog.Items) == 0 {
		return
	}
	catalog.FetchedAt = c.now()
	c.store(agent, modelCacheEntry{Catalog: catalog, FetchedAt: catalog.FetchedAt})
}

func (c *modelCatalogCache) load(agent string) (modelCacheEntry, bool) {
	c.mu.Lock()
	entry, ok := c.mem[agent]
	c.mu.Unlock()
	if ok {
		return entry, true
	}
	path, err := c.path(agent)
	if err != nil {
		return modelCacheEntry{}, false
	}
	if err := readBoundedModelFile(path, &entry); err != nil || entry.FetchedAt.IsZero() {
		return modelCacheEntry{}, false
	}
	entry.Catalog.Agent = agent
	entry.Catalog.normalize()
	if len(entry.Catalog.Items) == 0 {
		return modelCacheEntry{}, false
	}
	c.mu.Lock()
	if current, ok := c.mem[agent]; ok {
		entry = current
	} else {
		c.mem[agent] = entry
	}
	c.mu.Unlock()
	return entry, true
}

func (c *modelCatalogCache) store(agent string, entry modelCacheEntry) {
	entry.Catalog.Cached = false
	entry.Catalog.RefreshFailed = false
	// A built-in fallback never replaces a list the agent really reported.
	if entry.Catalog.Fallback {
		if current, ok := c.load(agent); ok && !current.Catalog.Fallback {
			return
		}
	}
	c.mu.Lock()
	c.mem[agent] = entry
	c.mu.Unlock()
	// Built-in fallbacks stay in memory only so the next launch retries.
	if entry.Catalog.Fallback {
		return
	}
	path, err := c.path(agent)
	if err != nil {
		return
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), agent+"-*.tmp")
	if err != nil {
		return
	}
	_, werr := file.Write(raw)
	cerr := file.Close()
	if werr != nil || cerr != nil || os.Rename(file.Name(), path) != nil {
		_ = os.Remove(file.Name())
	}
}

func (c *modelCatalogCache) path(agent string) (string, error) {
	if !validCacheAgent(agent) {
		return "", errors.New("invalid agent for model cache")
	}
	dir, err := c.dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, agent+".json"), nil
}

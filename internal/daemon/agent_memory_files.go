package daemon

// agent_memory_files.go keeps a managed project-memory section in the
// workspace's CLAUDE.md / AGENTS.md / GEMINI.md (opt-in per workspace; see
// internal/agentfiles for the merge rules and settings storage).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"central-memory/internal/agentfiles"
)

// AgentFilesSyncEvery is the periodic sync cadence (at most once per tick).
const AgentFilesSyncEvery = 30 * time.Minute

// errBriefNotFound is returned (and swallowed) when the server has no brief
// route or no brief for the project.
var errBriefNotFound = fmt.Errorf("daemon: project brief not found")

func cloudGetJSON(ctx context.Context, client *http.Client, endpoint, token string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("daemon: GET %s: %s", endpoint, resp.Status)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return resp.StatusCode, fmt.Errorf("daemon: decode %s: %w", endpoint, err)
	}
	return resp.StatusCode, nil
}

// FetchProjectBrief reads GET /v1/projects/{id}/brief. A 404 (route not
// deployed or no brief) returns (nil, nil).
func (s *HTTPMemoryStore) FetchProjectBrief(ctx context.Context) (*agentfiles.Brief, error) {
	base, token, projectID, client, err := s.mirrorConnection()
	if err != nil {
		return nil, err
	}
	var out agentfiles.Brief
	status, err := cloudGetJSON(ctx, client, base+"/v1/projects/"+url.PathEscape(projectID)+"/brief", token, &out)
	if status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.Content) == "" {
		return nil, nil
	}
	return &out, nil
}

// scopeRank orders memories: decisions first, then constraints, patterns,
// preferences, facts. Episode summaries are excluded.
func scopeRank(scope string) int {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "decision":
		return 0
	case "constraint":
		return 1
	case "pattern":
		return 2
	case "preference":
		return 3
	case "fact", "":
		return 4
	}
	return -1
}

// FetchTopProjectMemories returns up to limit CONFIRMED project-level
// memories (decisions first, then by use count and recency) via
// GET /memory/search?project_id=..&level=project.
func (s *HTTPMemoryStore) FetchTopProjectMemories(ctx context.Context, limit int) ([]agentfiles.Memory, error) {
	if limit <= 0 {
		limit = agentfiles.MaxMemories
	}
	base, token, projectID, client, err := s.mirrorConnection()
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("project_id", projectID)
	q.Set("level", "project")
	q.Set("limit", "100")
	var out struct {
		Items []struct {
			ID        string    `json:"id"`
			Key       string    `json:"key"`
			Content   string    `json:"content"`
			Level     string    `json:"level"`
			Scope     string    `json:"scope"`
			Status    string    `json:"status"`
			UseCount  int       `json:"use_count"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"items"`
	}
	if _, err := cloudGetJSON(ctx, client, base+"/memory/search?"+q.Encode(), token, &out); err != nil {
		return nil, err
	}
	type ranked struct {
		m    agentfiles.Memory
		rank int
		use  int
		at   time.Time
	}
	var items []ranked
	for _, it := range out.Items {
		if !strings.EqualFold(it.Status, "CONFIRMED") {
			continue
		}
		if it.Level != "" && !strings.EqualFold(it.Level, "project") {
			continue
		}
		rank := scopeRank(it.Scope)
		if rank < 0 || strings.TrimSpace(it.Content) == "" {
			continue
		}
		items = append(items, ranked{
			m: agentfiles.Memory{
				ID: it.ID, Key: it.Key, Content: it.Content, Scope: it.Scope,
				UpdatedAt: it.UpdatedAt.UTC().Format(time.RFC3339Nano),
			},
			rank: rank, use: it.UseCount, at: it.UpdatedAt,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.use != b.use {
			return a.use > b.use
		}
		if !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		return a.m.ID < b.m.ID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	res := make([]agentfiles.Memory, len(items))
	for i, r := range items {
		res[i] = r.m
	}
	return res, nil
}

// AgentFilesSyncer syncs one workspace's agent instruction files.
type AgentFilesSyncer struct {
	Root  string
	Store *HTTPMemoryStore
	// LoadSettings defaults to agentfiles.GetSettings.
	LoadSettings func(root string) (agentfiles.Settings, error)
	// OnWrite is told about every file Nexus wrote (workspace-relative), so
	// the files-changed reporter can ignore its own writes.
	OnWrite func(rel string)

	mu       sync.Mutex
	lastHash string
	last     agentfiles.SyncReport
}

// Sync fetches the brief and top memories and updates the enabled files.
// Unless force is set, it does nothing when the fingerprint (brief
// updated_at/content, memory set, settings) matches the last sync.
func (a *AgentFilesSyncer) Sync(ctx context.Context, force bool) (agentfiles.SyncReport, error) {
	if a == nil || strings.TrimSpace(a.Root) == "" {
		return agentfiles.SyncReport{Skipped: "no workspace", At: time.Now().UTC()}, nil
	}
	report := agentfiles.SyncReport{Root: a.Root, At: time.Now().UTC()}
	a.mu.Lock()
	defer a.mu.Unlock()
	load := a.LoadSettings
	if load == nil {
		load = agentfiles.GetSettings
	}
	settings, err := load(a.Root)
	if err != nil {
		report.Skipped = "settings unreadable"
		return report, err
	}
	settings = settings.Normalize()
	report.Enabled = settings.Enabled
	if !settings.Enabled {
		report.Skipped = "disabled"
		a.lastHash = ""
		return report, nil
	}
	if a.Store == nil {
		report.Skipped = "not connected"
		return report, nil
	}
	if _, _, projectID, _, err := a.Store.mirrorConnection(); err != nil || !looksLikeUUID(projectID) {
		report.Skipped = "not connected"
		return report, nil
	}
	brief, err := a.Store.FetchProjectBrief(ctx)
	if err != nil {
		report.Skipped = "brief fetch failed"
		return report, err
	}
	mems, err := a.Store.FetchTopProjectMemories(ctx, agentfiles.MaxMemories)
	if err != nil {
		report.Skipped = "memory fetch failed"
		return report, err
	}
	report.BriefFound = brief != nil
	report.Memories = len(mems)
	report.Fingerprint = agentfiles.Fingerprint(brief, mems, settings)
	if !force && report.Fingerprint == a.lastHash {
		report.Skipped = "unchanged"
		return report, nil
	}
	body := agentfiles.Render(brief, mems)
	if a.OnWrite != nil {
		// Suppress before writing so the watcher never races ahead.
		for _, id := range settings.Files {
			a.OnWrite(agentfiles.FileName(id))
		}
	}
	report.Files = agentfiles.SyncWorkspace(a.Root, settings, body)
	clean := true
	for _, r := range report.Files {
		switch r.Action {
		case agentfiles.ActionCreated, agentfiles.ActionUpdated:
			log.Printf("daemon: agent memory files: %s %s", r.Action, filepath.Base(r.Path))
		case agentfiles.ActionError:
			clean = false
			log.Printf("daemon: agent memory files: %s: %s", filepath.Base(r.Path), r.Reason)
		case agentfiles.ActionSkipped:
			if r.Err != nil {
				log.Printf("daemon: agent memory files: skipped %s: %s", filepath.Base(r.Path), r.Reason)
			}
		}
	}
	if clean {
		a.lastHash = report.Fingerprint
	}
	a.last = report
	return report, nil
}

// SetOnWrite installs the OnWrite hook safely while syncs may be running.
func (a *AgentFilesSyncer) SetOnWrite(fn func(rel string)) {
	a.mu.Lock()
	a.OnWrite = fn
	a.mu.Unlock()
}

// LastReport returns the most recent completed sync report.
func (a *AgentFilesSyncer) LastReport() agentfiles.SyncReport {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// Run syncs once now and then every `every` (AgentFilesSyncEvery when <=0)
// until ctx ends. paused (optional) skips periodic syncs.
func (a *AgentFilesSyncer) Run(ctx context.Context, every time.Duration, paused func() bool) {
	if every <= 0 {
		every = AgentFilesSyncEvery
	}
	run := func() {
		if paused != nil && paused() {
			return
		}
		sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if _, err := a.Sync(sctx, false); err != nil && ctx.Err() == nil {
			log.Printf("daemon: agent memory files sync: %v", err)
		}
	}
	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// SyncAgentMemoryFiles syncs this workspace's agent instruction files now
// (bypassing the unchanged-fingerprint gate when force is true). Intended
// for the core's agents.memory_files.sync method.
func (r *Runtime) SyncAgentMemoryFiles(ctx context.Context, force bool) (agentfiles.SyncReport, error) {
	if r == nil || r.Daemon == nil {
		return agentfiles.SyncReport{Skipped: "no workspace"}, nil
	}
	return r.agentFilesSyncer().Sync(ctx, force)
}

func (r *Runtime) agentFilesSyncer() *AgentFilesSyncer {
	r.extraMu.Lock()
	defer r.extraMu.Unlock()
	if r.agentFiles == nil {
		hs, _ := r.httpStore()
		r.agentFiles = &AgentFilesSyncer{Root: r.Daemon.Root, Store: hs}
	}
	return r.agentFiles
}

// startCloudSideFeatures starts the project file watcher + files-changed
// reporter, the code-links reporter and the agent-files syncer when the
// runtime uploads to a server.
// Each runs in its own goroutine tracked by wg.
func (r *Runtime) startCloudSideFeatures(ctx context.Context, wg *sync.WaitGroup) {
	hs, ok := r.httpStore()
	if !ok || hs == nil || r.Daemon == nil || strings.TrimSpace(r.Daemon.Root) == "" {
		return
	}
	root := r.Daemon.Root
	rep := NewFilesChangedReporter(root, hs.mirrorConnection)
	links := NewCodeLinksReporter(root, hs.mirrorConnection)
	r.extraMu.Lock()
	r.filesChanged = rep
	r.codeLinks = links
	r.extraMu.Unlock()
	syncer := r.agentFilesSyncer()
	syncer.SetOnWrite(func(rel string) { rep.Suppress(rel, time.Minute) })

	wg.Add(5)
	go func() {
		defer wg.Done()
		rep.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		links.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		// One-time line-history replay of recent transcripts.
		r.backfillCodeHunks(ctx, links)
	}()
	go func() {
		defer wg.Done()
		runProjectWatcher(ctx, root, rep, r.Paused)
	}()
	go func() {
		defer wg.Done()
		syncer.Run(ctx, AgentFilesSyncEvery, r.Paused)
	}()
}

// SyncAgentMemoryFilesFor runs a one-off sync for root against an explicit
// cloud connection (for callers without a Runtime). It always forces: a
// fresh syncer has no previous fingerprint. Settings are still honoured.
func SyncAgentMemoryFilesFor(ctx context.Context, root, serverURL, token, projectID string) (agentfiles.SyncReport, error) {
	a := &AgentFilesSyncer{Root: root, Store: NewHTTPMemoryStore(serverURL, token, projectID)}
	return a.Sync(ctx, true)
}

// Command daemon-bootstrap is the production entrypoint for the workspace
// daemon container (issue #20, plan §1.3).
//
// It boots the local-first daemon from internal/daemon against a workspace
// root: loads (or mints, mode 0600) the bearer token, serves the sandboxed
// file/git/command API, registers with the central server, and heartbeats
// every 30s. Empty CENTRAL_SERVER_URL means local-only mode (register and
// heartbeat are no-ops) — the container still serves /healthz unauthenticated
// for the orchestrator probe.
//
// Environment:
//
//	WORKSPACE_ROOT       workspace directory (default /workspace)
//	CENTRAL_SERVER_URL   e.g. https://api.example.com (optional)
//	DAEMON_ADDR          listen address (default :7687)
//	MACHINE_ID           override for os.Hostname() (optional)
//	PROJECT_ID           project the push materializer registers targets for
//	                     (empty = materializer runs with no targets; see run)
//	PUSH_TARGETS         push fan-out override, "agent:path,agent:path"
//	                     (default: registry seed fan-out — copilot, cursor,
//	                     windsurf files with their seed budgets/formats)
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"central-memory/internal/daemon"
	"central-memory/internal/materializer"
	"central-memory/internal/store"
)

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// sandboxFileStore routes materializer file I/O through the daemon's
// sandboxed helpers (issue #41: the FileStore→sandbox injection is now
// real, not interface-only). Traversal escapes, secret-pattern hits and
// the 1MB read cap are enforced by the exact code the daemon serves.
type sandboxFileStore struct{ root string }

// compile-time proof the sandbox adapter satisfies the materializer seam.
var _ materializer.FileStore = sandboxFileStore{}

// ReadFile implements materializer.FileReader.
func (s sandboxFileStore) ReadFile(path string) ([]byte, error) {
	return daemon.ReadFileSandboxed(s.root, path)
}

// WriteFile implements materializer.FileWriter.
func (s sandboxFileStore) WriteFile(path string, content []byte) error {
	return daemon.WriteFileSandboxed(s.root, path, content)
}

// storeMemorySource adapts store memory rows to materializer.Memory
// (issue #41). The list closure is the seam: the daemon container is
// local-first with no database of its own, so the shipped closure yields
// no rows yet — but the CONFIRMED-only filter and the store.MemoryItem →
// materializer.Memory field mapping are real, and the live store query +
// Subscribe→HandleEvent adapter lands as a follow-up swapping only the
// closure body.
type storeMemorySource struct {
	list func(ctx context.Context, projectID string) ([]store.MemoryItem, error)
}

// ListMemories implements materializer.MemorySource.
func (s storeMemorySource) ListMemories(ctx context.Context, projectID string) ([]materializer.Memory, error) {
	if s.list == nil {
		return nil, nil
	}
	items, err := s.list(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]materializer.Memory, 0, len(items))
	for _, it := range items {
		if it.Status != store.StatusConfirmed {
			continue
		}
		out = append(out, materializer.Memory{
			Key:        it.Key,
			Content:    it.Content,
			Scope:      it.Scope,
			Level:      it.Level,
			Confidence: it.Confidence,
		})
	}
	return out, nil
}

// seedFormat returns the seed output format for a push agent ("markdown"
// for copilot, "text" for cursor/windsurf); unknown agents default to
// "text" (the materializer's plain-line template).
func seedFormat(agentName string) string {
	norm := store.NormalizeAgentName(agentName)
	for _, a := range store.SeedAgents {
		if store.NormalizeAgentName(a.Name) == norm && strings.TrimSpace(a.OutputFormat) != "" {
			return a.OutputFormat
		}
	}
	return "text"
}

// pushTargetsForProject resolves the materializer fan-out for one project
// (issue #41, plan §4.1): PUSH_TARGETS ("agent:path,...") overrides win,
// otherwise the registry seed fan-out (copilot/cursor/windsurf files via
// store.PushTargets). Budgets come from store.BudgetFor (per-agent seed
// values), formats from the seed rows. Malformed env entries are skipped
// with a log, never fatal. Sorted by output path for deterministic
// registration order.
func pushTargetsForProject(projectID string) []materializer.Target {
	paths := store.PushTargets(store.SeedAgents)
	if raw := strings.TrimSpace(os.Getenv("PUSH_TARGETS")); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			name, path, ok := strings.Cut(strings.TrimSpace(entry), ":")
			name, path = strings.TrimSpace(name), strings.TrimSpace(path)
			if !ok || name == "" || path == "" {
				log.Printf("daemon-bootstrap: ignoring malformed PUSH_TARGETS entry %q (want agent:path)", entry)
				continue
			}
			paths[store.NormalizeAgentName(name)] = path
		}
	}
	out := make([]materializer.Target, 0, len(paths))
	for name, path := range paths {
		out = append(out, materializer.Target{
			ProjectID:     projectID,
			OutputPath:    path,
			ContextBudget: store.BudgetFor(name),
			Format:        seedFormat(name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OutputPath < out[j].OutputPath })
	return out
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("daemon-bootstrap: %v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	root := getenv("WORKSPACE_ROOT", "/workspace")
	serverURL := strings.TrimSpace(os.Getenv("CENTRAL_SERVER_URL"))
	addr := getenv("DAEMON_ADDR", ":7687")

	d, err := daemon.New(root, serverURL, addr)
	if err != nil {
		return err
	}
	if mid := strings.TrimSpace(os.Getenv("MACHINE_ID")); mid != "" {
		d.MachineID = mid
	}

	bound, err := d.Start(ctx)
	if err != nil {
		return err
	}
	log.Printf("daemon-bootstrap: serving workspace root=%s addr=%s machine=%s server=%q",
		d.Root, bound, d.MachineID, d.ServerURL)

	if d.ServerURL != "" {
		if err := d.Register(ctx); err != nil {
			// Registration failure must not kill the daemon: local file/git
			// ops stay available and the heartbeat loop retries presence.
			log.Printf("daemon-bootstrap: initial register failed (will retry via heartbeat): %v", err)
		}
		d.StartHeartbeatLoop(ctx)
		defer d.StopHeartbeat()
	} else {
		log.Print("daemon-bootstrap: CENTRAL_SERVER_URL empty — local-only mode (no register/heartbeat)")
	}

	// Issue #41: construct the push-model materializer (previously never
	// built in prod). File I/O is the real daemon sandbox; the memory
	// source is store-shaped (live query + event subscription follow-up);
	// targets come from the push config (seed fan-out or PUSH_TARGETS).
	mat, err := materializer.New(
		storeMemorySource{list: func(context.Context, string) ([]store.MemoryItem, error) {
			return nil, nil
		}},
		sandboxFileStore{root: d.Root},
		nil, // SystemClock
		0,   // DefaultDebounce (5s quiet period, plan §4.2)
	)
	if err != nil {
		return err
	}
	mat.SetSandboxRoot(d.Root)
	if projectID := strings.TrimSpace(os.Getenv("PROJECT_ID")); projectID != "" {
		targets := pushTargetsForProject(projectID)
		added := 0
		for _, t := range targets {
			if mat.AddTarget(t) {
				added++
			} else {
				log.Printf("daemon-bootstrap: materializer rejected target %q (sandbox/duplicate)", t.OutputPath)
			}
		}
		log.Printf("daemon-bootstrap: materializer targets project=%s registered=%d/%d", projectID, added, len(targets))
	} else {
		log.Print("daemon-bootstrap: PROJECT_ID empty — push materializer runs with no targets (registration awaits project binding)")
	}
	go mat.Run(ctx)

	<-ctx.Done()
	log.Print("daemon-bootstrap: shutdown signal received, draining")
	d.Wait()
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	return nil
}

package server

// routes_extra.go — Phase 3/5 + confirmation-flow endpoints (nexus issue #8).
//
// registerRoutes() in routes.go only mounts the Phase 1.8 surface, so the
// CLI (cmd/nexus/session.go, branch.go, memory.go) and the dashboard
// (web/app.js) 404 on every route below. The Store methods already exist
// (internal/store/sessions.go, branches.go, store.go); this file only wires
// HTTP handlers + route bindings onto s.Mux. Kept in a separate file so
// parallel work on routes.go does not clash.
//
// Client-shape contract (server follows the callers, not the reverse):
//   - Sessions: CLI list parses {"items","count"} from GET /sessions;
//     create POSTs {"title","project_id"}; join POSTs {} to
//     /sessions/{id}/join.
//   - Branches: CLI list parses {"items","count"} from GET /branches;
//     fork POSTs {"name","project_id","from"}; checkout POSTs {} to
//     /branches/{name}/checkout; diff GETs /branches/diff with
//     ?project_id=&target=; merge POSTs {"source","target","project_id"}.
//   - Memory: CLI confirm/reject POST {} to /memory/{id}/confirm|reject;
//     dashboard memoryAction() POSTs {} to /memory/{id}/confirm|reject|promote.
//   - Episodes: resolve POSTs {"resolution","verification"} (both optional)
//     to /episodes/{id}/resolve; Store.ResolveEpisode already exists.
//
// Known limitations (see docs/decisions/2026-09-17-fix-missing-routes.md):
//   - RejectMemory does not exist on store.Store, so reject falls back to
//     mutating the fetched item (persists on MemStore, which returns a live
//     pointer; a PostgresStore needs a real RejectMemory method).
//   - Diff/merge enumerate each branch's OWN rows (ListBranchItems):
//     inherited parent/main-line keys are not included, so a fresh child
//     diffs empty until it is written to (CoW zero-copy consequence).
//   - Merge reports source deletions without removing target rows: no
//     branch-item delete primitive exists. Superseded target values are
//     shadowed by the new PROPOSED row, not marked SUPERSEDED.
//   - Branch checkout persists only when ?workspace_id= is given; without it
//     the endpoint is resolve-only and says so in the note field.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"central-memory/internal/branches"
	"central-memory/internal/store"
)

// registerExtraRoutes mounts the issue-#8 endpoints. Called from
// registerRoutes (routes.go); every route requires auth, matching the
// Phase 1.8 convention (/auth/login and /healthz stay public).
func (s *Server) registerExtraRoutes() {
	// Sessions (Phase 3).
	s.Mux.HandleFunc("GET /sessions", s.requireAuth(s.handleSessionList))
	s.Mux.HandleFunc("POST /sessions", s.requireAuth(s.handleSessionCreate))
	s.Mux.HandleFunc("POST /sessions/{id}/join", s.requireAuth(s.handleSessionJoin))

	// Branches (Phase 5). NOTE: "GET /branches/diff" must be registered as
	// its own pattern; "GET /branches" matches exactly /branches only.
	s.Mux.HandleFunc("GET /branches", s.requireAuth(s.handleBranchList))
	s.Mux.HandleFunc("POST /branches", s.requireAuth(s.handleBranchCreate))
	s.Mux.HandleFunc("POST /branches/{name}/checkout", s.requireAuth(s.handleBranchCheckout))
	s.Mux.HandleFunc("GET /branches/diff", s.requireAuth(s.handleBranchDiff))
	s.Mux.HandleFunc("POST /branches/merge", s.requireAuth(s.handleBranchMerge))

	// Confirmation flow (plan §2.8).
	s.Mux.HandleFunc("POST /memory/{id}/confirm", s.requireAuth(s.handleMemoryConfirm))
	s.Mux.HandleFunc("POST /memory/{id}/reject", s.requireAuth(s.handleMemoryReject))
	s.Mux.HandleFunc("POST /memory/{id}/promote", s.requireAuth(s.handleMemoryPromote))

	// Episodes.
	s.Mux.HandleFunc("POST /episodes/{id}/resolve", s.requireAuth(s.handleEpisodeResolve))
}

// sessionStore returns the session surface when the backing store
// implements it (both MemStore and PostgresStore do). A Store that does
// not implement SessionStore yields 501, not 500: the route exists but the
// backend cannot serve it.
func (s *Server) sessionStore() (store.SessionStore, bool) {
	ss, ok := s.Store.(store.SessionStore)
	return ss, ok
}

// branchStore is the BranchStore analogue of sessionStore.
func (s *Server) branchStore() (store.BranchStore, bool) {
	bs, ok := s.Store.(store.BranchStore)
	return bs, ok
}

// isInputError reports store validation failures that must surface as 400
// rather than 500 (missing fields, bad enum, depth cap).
func isInputError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "is required") ||
		strings.Contains(msg, "invalid ") ||
		strings.Contains(msg, "max branch depth") ||
		strings.Contains(msg, "must be")
}

// authSubject returns the JWT subject stashed by requireAuth.
func authSubject(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Auth-Subject"))
}

// --- sessions ---

func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id query parameter is required")
		return
	}
	ss, ok := s.sessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "sessions are not supported by this store")
		return
	}
	activeOnly := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("active_only")), "true")
	items, err := ss.ListProjectSessions(r.Context(), projectID, activeOnly)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list sessions: "+err.Error())
		return
	}
	if items == nil {
		items = []*store.Session{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

type sessionCreateRequest struct {
	Title     string `json:"title"`
	ProjectID string `json:"project_id"`
	CreatedBy string `json:"created_by"`
}

func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var req sessionCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	ss, ok := s.sessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "sessions are not supported by this store")
		return
	}
	createdBy := strings.TrimSpace(req.CreatedBy)
	if createdBy == "" {
		createdBy = authSubject(r)
	}
	sess := &store.Session{
		ProjectID: strings.TrimSpace(req.ProjectID),
		Title:     strings.TrimSpace(req.Title),
		CreatedBy: createdBy,
	}
	if err := ss.CreateSession(r.Context(), sess); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if isInputError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create session: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

type sessionJoinRequest struct {
	UserID  string `json:"user_id"`
	AgentID string `json:"agent_id"`
	Role    string `json:"role"`
}

func (s *Server) handleSessionJoin(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "session id path parameter is required")
		return
	}
	var req sessionJoinRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ss, ok := s.sessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "sessions are not supported by this store")
		return
	}
	// The CLI joins with an empty body {}; attribute the join to the
	// authenticated subject so the membership row is never anonymous.
	userID := strings.TrimSpace(req.UserID)
	agentID := strings.TrimSpace(req.AgentID)
	if userID == "" && agentID == "" {
		userID = authSubject(r)
	}
	p, err := ss.JoinSession(r.Context(), id, userID, agentID, req.Role)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if isInputError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.Contains(err.Error(), "is not active") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not join session: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// --- branches ---

func (s *Server) handleBranchList(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id query parameter is required")
		return
	}
	bs, ok := s.branchStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "branches are not supported by this store")
		return
	}
	if _, err := s.Store.GetProject(r.Context(), projectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load project: "+err.Error())
		return
	}
	// Get-or-create "main" so a fresh project lists one branch instead of
	// zero; matches EnsureMainBranch's documented purpose (covers projects
	// created after migration 005 seeded the old ones).
	if _, err := bs.EnsureMainBranch(r.Context(), projectID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not ensure main branch: "+err.Error())
		return
	}
	items, err := bs.ListBranches(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list branches: "+err.Error())
		return
	}
	if items == nil {
		items = []*store.MemoryBranch{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

type branchCreateRequest struct {
	Name       string `json:"name"`
	ProjectID  string `json:"project_id"`
	From       string `json:"from"`
	Visibility string `json:"visibility"`
	OwnerID    string `json:"owner_id"`
}

// findBranchByName resolves a branch name within one project. Branch names
// are unique per project (ForkBranch enforces ErrConflict), so the first
// match is the match.
func findBranchByName(branches []*store.MemoryBranch, name string) *store.MemoryBranch {
	for _, br := range branches {
		if br.Name == name {
			return br
		}
	}
	return nil
}

func (s *Server) handleBranchCreate(w http.ResponseWriter, r *http.Request) {
	var req branchCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	projectID := strings.TrimSpace(req.ProjectID)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	bs, ok := s.branchStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "branches are not supported by this store")
		return
	}
	if _, err := s.Store.GetProject(r.Context(), projectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load project: "+err.Error())
		return
	}
	// Resolve the fork source: blank "from" (and explicit "main") mean the
	// project's main branch; anything else is a sibling branch name.
	from := strings.TrimSpace(req.From)
	var parentID string
	if from == "" || from == store.MainBranchName {
		main, err := bs.EnsureMainBranch(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not ensure main branch: "+err.Error())
			return
		}
		parentID = main.ID
	} else {
		branches, err := bs.ListBranches(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not list branches: "+err.Error())
			return
		}
		parent := findBranchByName(branches, from)
		if parent == nil {
			writeError(w, http.StatusNotFound, "source branch "+from+" not found")
			return
		}
		parentID = parent.ID
	}
	owner := strings.TrimSpace(req.OwnerID)
	if owner == "" {
		owner = authSubject(r)
	}
	child, err := bs.ForkBranch(r.Context(), parentID, name, owner, req.Visibility, 0)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "branch "+name+" already exists")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "parent branch not found")
			return
		}
		if isInputError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not fork branch: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, child)
}

func (s *Server) handleBranchCheckout(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "branch name path parameter is required")
		return
	}
	bs, ok := s.branchStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "branches are not supported by this store")
		return
	}
	// Prefer an ID lookup (IDs are unambiguous across projects); fall back
	// to a name lookup scoped by ?project_id=. The request body ({} from the
	// CLI) carries no scope and is intentionally ignored.
	var br *store.MemoryBranch
	if b, err := bs.GetBranch(r.Context(), name); err == nil {
		br = b
	} else {
		projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
		if projectID == "" {
			writeError(w, http.StatusNotFound, "branch "+name+" not found (pass ?project_id= to resolve by name)")
			return
		}
		branches, err := bs.ListBranches(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not list branches: "+err.Error())
			return
		}
		if b := findBranchByName(branches, name); b != nil {
			br = b
		} else {
			writeError(w, http.StatusNotFound, "branch "+name+" not found")
			return
		}
	}
	// Workspace-scoped checkout mutation (issue #104): with ?workspace_id=
	// the branch name is persisted on the workspace row via
	// SetWorkspaceBranch. Without it the lookup stays resolve-only and the
	// note says so explicitly.
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	if workspaceID == "" {
		writeJSON(w, http.StatusOK, branchCheckoutView(br, "",
			"resolve-only: no ?workspace_id= given, nothing was persisted; pass ?workspace_id= to persist the active branch on a workspace"))
		return
	}
	if err := bs.SetWorkspaceBranch(r.Context(), workspaceID, br.Name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace "+workspaceID+" not found")
			return
		}
		if isInputError(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not persist checkout: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, branchCheckoutView(br, workspaceID,
		"active branch persisted on workspace "+workspaceID))
}

// branchCheckoutView flattens the branch fields so clients decoding the body
// as a MemoryBranch (the pre-#104 shape) keep working; the sibling
// workspace_id/note fields carry the mutation outcome.
func branchCheckoutView(br *store.MemoryBranch, workspaceID, note string) map[string]any {
	return map[string]any{
		"id":                 br.ID,
		"project_id":         br.ProjectID,
		"name":               br.Name,
		"owner_id":           br.OwnerID,
		"parent_branch_id":   br.ParentBranchID,
		"forked_at_event_id": br.ForkedAtEventID,
		"visibility":         br.Visibility,
		"created_at":         br.CreatedAt,
		"workspace_id":       workspaceID,
		"note":               note,
	}
}

// branchEntries adapts store rows to the stdlib-only branches.Entry shape
// (branch semantics need only key + content).
func branchEntries(items []*store.MemoryItem) []branches.Entry {
	out := make([]branches.Entry, 0, len(items))
	for _, m := range items {
		if m == nil {
			continue
		}
		out = append(out, branches.Entry{Key: m.Key, Content: m.Content})
	}
	return out
}

func (s *Server) handleBranchDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	projectID := strings.TrimSpace(q.Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id query parameter is required")
		return
	}
	bs, ok := s.branchStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "branches are not supported by this store")
		return
	}
	targetName := strings.TrimSpace(q.Get("target"))
	if targetName == "" {
		writeError(w, http.StatusBadRequest, "target query parameter is required")
		return
	}
	sourceName := strings.TrimSpace(q.Get("source"))
	if sourceName == "" {
		sourceName = store.MainBranchName
	}
	branchesList, err := bs.ListBranches(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list branches: "+err.Error())
		return
	}
	source := findBranchByName(branchesList, sourceName)
	if source == nil {
		// Accept raw branch IDs too (dashboard holds IDs, CLI holds names).
		if b, gerr := bs.GetBranch(r.Context(), sourceName); gerr == nil && b.ProjectID == projectID {
			source = b
		}
	}
	target := findBranchByName(branchesList, targetName)
	if target == nil {
		if b, gerr := bs.GetBranch(r.Context(), targetName); gerr == nil && b.ProjectID == projectID {
			target = b
		}
	}
	if source == nil {
		writeError(w, http.StatusNotFound, "source branch "+sourceName+" not found")
		return
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "target branch "+targetName+" not found")
		return
	}
	// Real key-level diff over each branch's own rows (ListBranchItems):
	// added = keys only on target, removed = keys only on source, modified
	// = same key with different content hash. Slices are normalized to []
	// (never null) to preserve the endpoint shape.
	sourceItems, err := bs.ListBranchItems(r.Context(), source.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list source branch items: "+err.Error())
		return
	}
	targetItems, err := bs.ListBranchItems(r.Context(), target.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list target branch items: "+err.Error())
		return
	}
	d := branches.DiffBranches(branchEntries(sourceItems), branchEntries(targetItems))
	if d.Added == nil {
		d.Added = []branches.Entry{}
	}
	if d.Removed == nil {
		d.Removed = []branches.Entry{}
	}
	if d.Modified == nil {
		d.Modified = []branches.Change{}
	}
	if d.Unchanged == nil {
		d.Unchanged = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": projectID,
		"source":     source.Name,
		"source_id":  source.ID,
		"target":     target.Name,
		"target_id":  target.ID,
		"added":      d.Added,
		"removed":    d.Removed,
		"modified":   d.Modified,
		"unchanged":  d.Unchanged,
	})
}

type branchMergeRequest struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	ProjectID string `json:"project_id"`
}

func (s *Server) handleBranchMerge(w http.ResponseWriter, r *http.Request) {
	var req branchMergeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sourceRef := strings.TrimSpace(req.Source)
	if sourceRef == "" {
		writeError(w, http.StatusBadRequest, "source is required")
		return
	}
	targetRef := strings.TrimSpace(req.Target)
	if targetRef == "" {
		targetRef = store.MainBranchName
	}
	bs, ok := s.branchStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "branches are not supported by this store")
		return
	}
	projectID := strings.TrimSpace(req.ProjectID)
	resolve := func(ref string) (*store.MemoryBranch, error) {
		if projectID != "" {
			branches, err := bs.ListBranches(r.Context(), projectID)
			if err != nil {
				return nil, err
			}
			if br := findBranchByName(branches, ref); br != nil {
				return br, nil
			}
		}
		return bs.GetBranch(r.Context(), ref)
	}
	source, err := resolve(sourceRef)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "source branch "+sourceRef+" not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not resolve source branch: "+err.Error())
		return
	}
	target, err := resolve(targetRef)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "target branch "+targetRef+" not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not resolve target branch: "+err.Error())
		return
	}
	if projectID == "" {
		projectID = source.ProjectID
	}
	// Real 3-way merge: base is the source's parent-branch snapshot when the
	// source was forked from another branch, else empty (every source key
	// then counts as a source-side addition). Auto-merged keys are written
	// to the target as PROPOSED rows via WriteToBranch; conflicts are
	// returned verbatim and nothing is written for them.
	sourceItems, err := bs.ListBranchItems(r.Context(), source.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list source branch items: "+err.Error())
		return
	}
	targetItems, err := bs.ListBranchItems(r.Context(), target.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list target branch items: "+err.Error())
		return
	}
	var baseItems []*store.MemoryItem
	if strings.TrimSpace(source.ParentBranchID) != "" {
		baseItems, err = bs.ListBranchItems(r.Context(), source.ParentBranchID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not list base branch items: "+err.Error())
			return
		}
	}
	res := branches.Merge(branchEntries(baseItems), branchEntries(sourceItems), branchEntries(targetItems))
	mergedKeys := make([]string, 0, len(res.Merged))
	for _, m := range res.Merged {
		if werr := bs.WriteToBranch(r.Context(), target.ID, &store.MemoryItem{
			Key:     m.Key,
			Content: m.Content,
			Status:  branches.StatusProposed,
		}); werr != nil {
			writeError(w, http.StatusInternalServerError, "could not write merged item "+m.Key+": "+werr.Error())
			return
		}
		mergedKeys = append(mergedKeys, m.Key)
	}
	conflicts := res.Conflicts
	if conflicts == nil {
		conflicts = []branches.Conflict{}
	}
	superseded := res.Superseded
	if superseded == nil {
		superseded = []branches.SupersededMark{}
	}
	deleted := res.Deleted
	if deleted == nil {
		deleted = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source":       source.Name,
		"source_id":    source.ID,
		"target":       target.Name,
		"target_id":    target.ID,
		"project_id":   projectID,
		"merged":       true,
		"merged_count": len(mergedKeys),
		"merged_keys":  mergedKeys,
		"conflicts":    conflicts,
		"superseded":   superseded,
		"deleted":      deleted,
		"note": "wrote merged keys to target as PROPOSED rows; " +
			"superseded target values are shadowed by the new row, not marked SUPERSEDED; " +
			"deleted keys are reported, not removed (no branch-item delete primitive exists)",
	})
}

// --- memory confirmation flow ---

type memoryDecisionRequest struct {
	ConfirmedBy string `json:"confirmed_by"`
	RejectedBy  string `json:"rejected_by"`
}

func (s *Server) handleMemoryConfirm(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	var req memoryDecisionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	confirmedBy := strings.TrimSpace(req.ConfirmedBy)
	if confirmedBy == "" {
		confirmedBy = authSubject(r)
	}
	if err := s.Store.ConfirmMemory(r.Context(), id, confirmedBy); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not confirm memory: "+err.Error())
		return
	}
	item, err := s.Store.GetMemoryItem(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "CONFIRMED"})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// rejectMemoryStore is implemented by stores that natively support
// rejection. Neither MemStore nor PostgresStore does yet; the handler
// probes for it so a future Store implementation is picked up without a
// handler change.
type rejectMemoryStore interface {
	RejectMemory(ctx context.Context, id, rejectedBy string) error
}

func (s *Server) handleMemoryReject(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	var req memoryDecisionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	rejectedBy := strings.TrimSpace(req.RejectedBy)
	if rejectedBy == "" {
		rejectedBy = authSubject(r)
	}
	if rs, ok := s.Store.(rejectMemoryStore); ok {
		if err := rs.RejectMemory(r.Context(), id, rejectedBy); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "memory not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "could not reject memory: "+err.Error())
			return
		}
		item, err := s.Store.GetMemoryItem(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "REJECTED"})
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	// Fallback: MemStore.GetMemoryItem returns the live map pointer, so a
	// status flip persists without a new Store method. (PostgresStore
	// returns a scanned copy, so this fallback does NOT persist there —
	// that backend needs a real RejectMemory; see the decision doc.)
	// MemoryItem has no RejectedBy column; only the status transition is
	// recorded. _ = rejectedBy keeps the attribution hook visible.
	_ = rejectedBy
	item, err := s.Store.GetMemoryItem(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load memory: "+err.Error())
		return
	}
	item.Status = "REJECTED"
	item.UpdatedAt = time.Now().UTC()
	writeJSON(w, http.StatusOK, item)
}

// --- episodes ---

type episodeResolveRequest struct {
	Resolution   string `json:"resolution"`
	Verification string `json:"verification"`
	ResolvedBy   string `json:"resolved_by"`
}

func (s *Server) handleEpisodeResolve(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "episode id path parameter is required")
		return
	}
	var req episodeResolveRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resolvedBy := strings.TrimSpace(req.ResolvedBy)
	if resolvedBy == "" {
		resolvedBy = authSubject(r)
	}
	if err := s.Store.ResolveEpisode(r.Context(), id, req.Resolution, req.Verification, resolvedBy); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "episode not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not resolve episode: "+err.Error())
		return
	}
	ep, err := s.Store.GetEpisode(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "RESOLVED"})
		return
	}
	writeJSON(w, http.StatusOK, ep)
}

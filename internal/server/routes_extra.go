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
// Known behaviors (see docs/decisions/2026-09-17-fix-missing-routes.md):
//   - RejectMemory exists on store.Store backends (Wave 1), so reject takes
//     the native persistent path; terminal rows fail with 409.
//   - Diff/merge enumerate real branch content via branches.ListBranchContents
//     (SearchMemory universe + ResolveRead views) and apply merge results
//     through WriteToBranch, with deletions as SUPERSEDED tombstones.
//   - Branch checkout persists the project-scoped active-branch pointer
//     (Server.checkouts); unknown branches 404.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	s.Mux.HandleFunc("POST /sessions/{id}/leave", s.requireAuth(s.handleSessionLeave))

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

	// Phase 2 memory editing + version history (issue #162).
	s.registerMemoryEditRoutes()

	// Memory sharing + cross-project copy (issue #164 / Phase 4).
	s.Mux.HandleFunc("POST /memory/{id}/share", s.requireAuth(s.handleMemoryShare))
	s.Mux.HandleFunc("GET /memory/{id}/shares", s.requireAuth(s.handleMemorySharesList))
	s.Mux.HandleFunc("DELETE /memory/{id}/share/{userId}", s.requireAuth(s.handleMemoryUnshare))
	s.Mux.HandleFunc("POST /memory/{id}/copy", s.requireAuth(s.handleMemoryCopy))

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

// authSubject returns the JWT subject stashed by requireAuth: the canonical
// user UUID (issue #140) for Postgres ownership columns.
func authSubject(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Auth-Subject"))
}

// authUsername returns the display username claim stashed by requireAuth
// ("" for legacy tokens minted before the name claim existed).
func authUsername(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Auth-User"))
}

// --- sessions ---

// authorizeSession resolves a session and enforces project membership on
// its project (issue #141): session IDs are not authorization scope. It
// returns the session for handlers that need it. Unknown sessions 404;
// non-members 403.
func (s *Server) authorizeSession(w http.ResponseWriter, r *http.Request, sessionID string) (*store.Session, bool) {
	ss, ok := s.sessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "sessions are not supported by this store")
		return nil, false
	}
	sess, err := ss.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, "could not load session: "+err.Error())
		return nil, false
	}
	if !s.authorizeProject(w, r, sess.ProjectID) {
		return nil, false
	}
	return sess, true
}

func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if !s.authorizeProject(w, r, projectID) {
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
	if !s.authorizeProject(w, r, req.ProjectID) {
		return
	}
	ss, ok := s.sessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "sessions are not supported by this store")
		return
	}
	// Attribution is the authenticated user (issue #141): a client-supplied
	// created_by would let anyone forge session ownership.
	sess := &store.Session{
		ProjectID: strings.TrimSpace(req.ProjectID),
		Title:     strings.TrimSpace(req.Title),
		CreatedBy: authSubject(r),
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
	if _, ok := s.authorizeSession(w, r, id); !ok {
		return
	}
	ss, ok := s.sessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "sessions are not supported by this store")
		return
	}
	// Joins are self-attribution only (issue #141): a client-supplied
	// user_id would let anyone forge membership rows for other users.
	// Agents join via agent_id, which the daemon owns.
	userID := authSubject(r)
	agentID := strings.TrimSpace(req.AgentID)
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

// handleSessionLeave stamps left_at on the caller's membership row
// (issue #78: POST /sessions/{id}/leave was 404).
func (s *Server) handleSessionLeave(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "session id path parameter is required")
		return
	}
	var req sessionJoinRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, ok := s.authorizeSession(w, r, id); !ok {
		return
	}
	ss, ok := s.sessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "sessions are not supported by this store")
		return
	}
	// Self-leave only (issue #141): callers cannot remove other users.
	userID := authSubject(r)
	agentID := strings.TrimSpace(req.AgentID)
	if err := ss.LeaveSession(r.Context(), id, userID, agentID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session or membership not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not leave session: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "left": true})
}

// --- branches ---

// authorizeBranch enforces branch visibility on top of project membership
// (issue #148): shared branches are open to members; private branches
// require the owner. CheckBranchAccess was previously helper-only with no
// production call sites.
func (s *Server) authorizeBranch(w http.ResponseWriter, r *http.Request, br *store.MemoryBranch) bool {
	if err := store.CheckBranchAccess(br, authSubject(r)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "branch not found")
			return false
		}
		writeError(w, http.StatusForbidden, "private branch")
		return false
	}
	return true
}

func (s *Server) handleBranchList(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if !s.authorizeProject(w, r, projectID) {
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
	// Hide private branches the caller may not read (issue #148): the
	// list shows shared branches plus the caller's own.
	me := authSubject(r)
	visible := items[:0:0]
	for _, br := range items {
		if store.CheckBranchAccess(br, me) == nil {
			visible = append(visible, br)
		}
	}
	if visible == nil {
		visible = []*store.MemoryBranch{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": visible, "count": len(visible)})
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

// branchLoader adapts the store's BranchStore + SearchMemory to the
// branches.BranchLoader seam (issue #97): the project key universe comes
// from SearchMemory (capped), each key resolved through ResolveRead so
// branch overlays shadow ancestors.
type branchLoader struct {
	store interface {
		SearchMemory(ctx context.Context, projectID string, query string, tags []string, limit int) ([]*store.MemoryItem, error)
	}
	branches store.BranchStore
}

func (l branchLoader) Keys(ctx context.Context, projectID string) ([]string, error) {
	// Grow-until-stable (issue #134): SearchMemory has no offset, and a
	// fixed cap silently truncates diff/merge for projects with more keys
	// than the cap. Double the limit while pages come back full; stop at
	// the first short page (complete universe) or the hard cap.
	const maxKeysLimit = 65536
	items, err := l.store.SearchMemory(ctx, projectID, "", nil, 512)
	if err != nil {
		return nil, err
	}
	for limit := 1024; len(items) >= limit/2 && limit <= maxKeysLimit; limit *= 2 {
		next, err := l.store.SearchMemory(ctx, projectID, "", nil, limit)
		if err != nil {
			return nil, err
		}
		if len(next) == len(items) {
			items = next
			break
		}
		items = next
		if len(items) < limit {
			break
		}
	}
	seen := make(map[string]struct{}, len(items))
	keys := make([]string, 0, len(items))
	for _, it := range items {
		if it == nil || it.Key == "" {
			continue
		}
		if _, dup := seen[it.Key]; dup {
			continue
		}
		seen[it.Key] = struct{}{}
		keys = append(keys, it.Key)
	}
	return keys, nil
}

func (l branchLoader) Read(ctx context.Context, branchID, key string) (branches.Entry, error) {
	resolved, err := l.branches.ResolveRead(ctx, branchID, key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return branches.Entry{}, branches.ErrBranchKeyNotFound
		}
		return branches.Entry{}, err
	}
	// Terminal overlay rows (merge tombstones, rejections) hide the key:
	// history persists in the overlay, but the snapshot treats the key as
	// deleted — mirroring SearchMemory's CONFIRMED/PROPOSED-only rule.
	if resolved.Status == store.StatusSuperseded || resolved.Status == store.StatusRejected {
		return branches.Entry{}, branches.ErrBranchKeyNotFound
	}
	return branches.Entry{Key: resolved.Key, Content: resolved.Content}, nil
}

// branchSnapshot builds a branches.Entry snapshot for branchID (issue #97)
// via branches.ListBranchContents: the key universe comes from SearchMemory,
// each key resolved through ResolveRead so branch overlays shadow ancestors.
func (s *Server) branchSnapshot(ctx context.Context, projectID, branchID string) ([]branches.Entry, error) {
	bs, ok := s.branchStore()
	if !ok {
		return nil, errors.New("branches are not supported by this store")
	}
	return branches.ListBranchContents(ctx, branchLoader{store: s.Store, branches: bs}, projectID, branchID)
}

// branchBaseSnapshot reads the fork-parent snapshot for 3-way merge.
func (s *Server) branchBaseSnapshot(ctx context.Context, projectID string, source *store.MemoryBranch) ([]branches.Entry, error) {
	if source == nil || source.ParentBranchID == "" {
		return nil, nil
	}
	return s.branchSnapshot(ctx, projectID, source.ParentBranchID)
}

// applyMergeResult writes merged/deleted keys onto the target branch via
// WriteToBranch (issue #97: ListBranchContents + apply merge result).
// Per-key errors are aggregated, not fail-fast (issue #134): a single bad
// row must not leave a half-merged target silently — the caller reports
// which keys landed via the merged response plus the error.
func (s *Server) applyMergeResult(ctx context.Context, targetID string, result branches.MergeResult) error {
	bs, ok := s.branchStore()
	if !ok {
		return errors.New("branches are not supported by this store")
	}
	var errs []error
	for _, m := range result.Merged {
		if err := bs.WriteToBranch(ctx, targetID, &store.MemoryItem{
			Key: m.Key, Content: m.Content, Status: m.Status,
		}); err != nil {
			errs = append(errs, fmt.Errorf("write %s: %w", m.Key, err))
		}
	}
	// Deletions propagate as SUPERSEDED tombstones so the key disappears
	// from branch snapshots (terminal overlay rows read as not-found)
	// without losing history. The marker content satisfies the 20–2000
	// content CHECK (issue #134); tombstones are never rendered.
	for _, key := range result.Deleted {
		if err := bs.WriteToBranch(ctx, targetID, &store.MemoryItem{
			Key: key, Content: store.TombstoneContent, Status: store.StatusSuperseded,
		}); err != nil {
			errs = append(errs, fmt.Errorf("tombstone %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
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
	if !s.authorizeProject(w, r, projectID) {
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
	// A private source the caller cannot read cannot be forked (issue #148).
	from := strings.TrimSpace(req.From)
	var parentID string
	if from == "" || from == store.MainBranchName {
		main, err := bs.EnsureMainBranch(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not ensure main branch: "+err.Error())
			return
		}
		if !s.authorizeBranch(w, r, main) {
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
		if !s.authorizeBranch(w, r, parent) {
			return
		}
		parentID = parent.ID
	}
	// Ownership is the authenticated user (issue #141).
	owner := authSubject(r)
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

// activeBranchFor returns the checked-out branch ID for a project, or "".
func (s *Server) activeBranchFor(projectID string) string {
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	if s.checkouts == nil {
		s.checkouts = make(map[string]string)
	}
	return s.checkouts[projectID]
}

func (s *Server) setActiveBranch(projectID, branchID string) {
	s.checkoutMu.Lock()
	defer s.checkoutMu.Unlock()
	if s.checkouts == nil {
		s.checkouts = make(map[string]string)
	}
	s.checkouts[projectID] = branchID
}

// handleBranchCheckout resolves the branch (400/404 on unknown) and
// persists the active-branch pointer (issue #104). Contract: checkout is
// project-scoped by default (the active branch for ?project_id=, or for the
// resolved branch's own project when looked up by ID).
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
	// to a name lookup scoped by ?project_id=. Either way the resolved
	// branch's project is authorized (issue #141), then its visibility
	// (issue #148).
	if br, err := bs.GetBranch(r.Context(), name); err == nil {
		if !s.authorizeProject(w, r, br.ProjectID) {
			return
		}
		if !s.authorizeBranch(w, r, br) {
			return
		}
		s.setActiveBranch(br.ProjectID, br.ID)
		writeJSON(w, http.StatusOK, br)
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusNotFound, "branch "+name+" not found (pass ?project_id= to resolve by name)")
		return
	}
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	branches, err := bs.ListBranches(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list branches: "+err.Error())
		return
	}
	if br := findBranchByName(branches, name); br != nil {
		if !s.authorizeBranch(w, r, br) {
			return
		}
		s.setActiveBranch(projectID, br.ID)
		writeJSON(w, http.StatusOK, br)
		return
	}
	writeError(w, http.StatusNotFound, "branch "+name+" not found")
	return
}

func (s *Server) handleBranchDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	projectID := strings.TrimSpace(q.Get("project_id"))
	if !s.authorizeProject(w, r, projectID) {
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
	all, err := bs.ListBranches(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list branches: "+err.Error())
		return
	}
	source := findBranchByName(all, sourceName)
	if source == nil {
		// Accept raw branch IDs too (dashboard holds IDs, CLI holds names).
		if b, gerr := bs.GetBranch(r.Context(), sourceName); gerr == nil && b.ProjectID == projectID {
			source = b
		}
	}
	target := findBranchByName(all, targetName)
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
	// Visibility on both inputs (issue #148): snapshots read branch
	// content, so a private source or target is unreadable to non-owners.
	if !s.authorizeBranch(w, r, source) {
		return
	}
	if !s.authorizeBranch(w, r, target) {
		return
	}
	// Enumerate branch contents via SearchMemory + ResolveRead (issue #97):
	// project memories supply the key universe, ResolveRead resolves each
	// key's branch view, then branches.DiffBranches computes the key-level
	// diff over real store data.
	srcSnap, err := s.branchSnapshot(r.Context(), projectID, source.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read source branch: "+err.Error())
		return
	}
	tgtSnap, err := s.branchSnapshot(r.Context(), projectID, target.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read target branch: "+err.Error())
		return
	}
	diff := branches.DiffBranches(srcSnap, tgtSnap)
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": projectID,
		"source":     source.Name,
		"source_id":  source.ID,
		"target":     target.Name,
		"target_id":  target.ID,
		"added":      diff.Added,
		"removed":    diff.Removed,
		"modified":   diff.Modified,
		"unchanged":  diff.Unchanged,
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
			all, err := bs.ListBranches(r.Context(), projectID)
			if err != nil {
				return nil, err
			}
			if br := findBranchByName(all, ref); br != nil {
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
	// Same-project enforcement (issue #134): resolve-by-ID bypasses the
	// project-scoped name lookup, so verify explicitly — otherwise merges
	// write across projects.
	if source.ProjectID != target.ProjectID {
		writeError(w, http.StatusBadRequest, "source and target branches belong to different projects")
		return
	}
	if source.ProjectID != projectID || target.ProjectID != projectID {
		writeError(w, http.StatusBadRequest, "branches do not belong to the requested project")
		return
	}
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	// Visibility on both inputs (issue #148): merge reads both snapshots,
	// so a private source cannot be merged by a non-owner member.
	if !s.authorizeBranch(w, r, source) {
		return
	}
	if !s.authorizeBranch(w, r, target) {
		return
	}
	// 3-way merge over real snapshots (issue #97): base is the source's
	// parent snapshot (fork point approximation). When the parent cannot
	// be read, fall back to the target snapshot (documented 2-way merge:
	// source-only additions apply) instead of an empty base — an empty
	// base would misread every shared key as a source addition (issue #134).
	srcSnap, err := s.branchSnapshot(r.Context(), projectID, source.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read source branch: "+err.Error())
		return
	}
	tgtSnap, err := s.branchSnapshot(r.Context(), projectID, target.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read target branch: "+err.Error())
		return
	}
	baseSnap, err := s.branchBaseSnapshot(r.Context(), projectID, source)
	if err != nil {
		baseSnap = tgtSnap
	}
	result := branches.Merge(baseSnap, srcSnap, tgtSnap)
	if err := s.applyMergeResult(r.Context(), target.ID, result); err != nil {
		writeError(w, http.StatusInternalServerError, "could not apply merge: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source":       source.Name,
		"source_id":    source.ID,
		"target":       target.Name,
		"target_id":    target.ID,
		"project_id":   projectID,
		"merged":       len(result.Conflicts) == 0,
		"conflicts":    result.Conflicts,
		"merged_items": result.Merged,
		"superseded":   result.Superseded,
		"deleted":      result.Deleted,
	})
}

// --- memory confirmation flow ---

type memoryDecisionRequest struct {
	ConfirmedBy string `json:"confirmed_by"`
	RejectedBy  string `json:"rejected_by"`
}

// authorizeMemory resolves a memory and enforces membership + visibility
// (issues #141, #164): memory IDs are not authorization scope. PUBLIC rows
// are readable without project membership; other modes require CanViewMemory.
func (s *Server) authorizeMemory(w http.ResponseWriter, r *http.Request, id string) bool {
	item, err := s.Store.GetMemoryItem(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "memory not found")
			return false
		}
		writeError(w, http.StatusInternalServerError, "could not load memory: "+err.Error())
		return false
	}
	subject := authSubject(r)
	vis := store.NormalizeVisibility(item.Visibility)
	if vis == store.VisibilityPublic {
		return true
	}
	isMember := false
	if strings.TrimSpace(item.ProjectID) != "" {
		ok, merr := s.Store.IsProjectMember(r.Context(), subject, item.ProjectID)
		if merr != nil {
			writeError(w, http.StatusInternalServerError, "could not check project membership")
			return false
		}
		isMember = ok
	}
	hasShare := false
	if vis == store.VisibilityShared {
		hasShare = s.viewerHasMemoryShare(r, item, subject)
	}
	if !store.CanViewMemory(item, subject, isMember, hasShare) {
		writeError(w, http.StatusForbidden, "not authorized to access this memory")
		return false
	}
	return true
}

// memorySharingStore is implemented by MemStore + PostgresStore (issue #164).
type memorySharingStore interface {
	ShareMemory(ctx context.Context, memoryID, userID, role, sharedBy string) (*store.MemoryShare, error)
	UnshareMemory(ctx context.Context, memoryID, userID string) error
	ListMemoryShares(ctx context.Context, memoryID string) ([]*store.MemoryShare, error)
	SetMemoryVisibility(ctx context.Context, memoryID, visibility string) error
	CopyMemory(ctx context.Context, memoryID, targetProjectID, copiedBy string) (*store.MemoryItem, error)
}

func (s *Server) sharingStore() (memorySharingStore, bool) {
	ss, ok := s.Store.(memorySharingStore)
	return ss, ok
}

func (s *Server) viewerHasMemoryShare(r *http.Request, item *store.MemoryItem, subject string) bool {
	ss, ok := s.sharingStore()
	if !ok || item == nil || subject == "" {
		return false
	}
	shares, err := ss.ListMemoryShares(r.Context(), item.ID)
	if err != nil {
		return false
	}
	role := ""
	if item.ProjectID != "" {
		if p, err := s.Store.GetProject(r.Context(), item.ProjectID); err == nil && p != nil && p.CreatedBy == subject {
			role = "OWNER"
		} else if ms, ok := s.Store.(*store.MemStore); ok {
			role = ms.MemberRole(item.ProjectID, subject)
		} else if members, err := s.Store.ListMembers(r.Context(), item.ProjectID); err == nil {
			for _, m := range members {
				if m == subject {
					role = "MEMBER"
					break
				}
			}
		}
	}
	for _, sh := range shares {
		if sh.SharedWithUserID != "" && sh.SharedWithUserID == subject {
			return true
		}
		if sh.SharedWithRole != "" && role != "" && strings.EqualFold(sh.SharedWithRole, role) {
			return true
		}
	}
	return false
}

// filterMemoriesByVisibility applies issue #164 rules to vector-search
// results (SearchMemory already filters via WithViewer).
func (s *Server) filterMemoriesByVisibility(r *http.Request, items []*store.MemoryItem, subject string) []*store.MemoryItem {
	if len(items) == 0 {
		return items
	}
	out := make([]*store.MemoryItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		vis := store.NormalizeVisibility(item.Visibility)
		if vis == store.VisibilityPublic {
			out = append(out, item)
			continue
		}
		isMember := false
		if strings.TrimSpace(item.ProjectID) != "" {
			ok, err := s.Store.IsProjectMember(r.Context(), subject, item.ProjectID)
			if err == nil {
				isMember = ok
			}
		}
		hasShare := false
		if vis == store.VisibilityShared {
			hasShare = s.viewerHasMemoryShare(r, item, subject)
		}
		if store.CanViewMemory(item, subject, isMember, hasShare) {
			out = append(out, item)
		}
	}
	return out
}

type memoryShareRequest struct {
	UserID     string `json:"user_id"`
	Role       string `json:"role"`
	Visibility string `json:"visibility"`
}

func (s *Server) handleMemoryShare(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	item, err := s.Store.GetMemoryItem(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load memory: "+err.Error())
		return
	}
	if !s.authorizeProject(w, r, item.ProjectID) {
		return
	}
	subject := authSubject(r)
	ss, ok := s.sharingStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory sharing not supported by configured store")
		return
	}
	var req memoryShareRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if v := strings.TrimSpace(req.Visibility); v != "" {
		if !store.IsValidVisibility(v) {
			writeError(w, http.StatusBadRequest, "visibility must be private|shared|project|public")
			return
		}
		if err := ss.SetMemoryVisibility(r.Context(), id, v); err != nil {
			writeError(w, http.StatusInternalServerError, "could not set visibility: "+err.Error())
			return
		}
		if strings.TrimSpace(req.UserID) == "" && strings.TrimSpace(req.Role) == "" {
			item, _ = s.Store.GetMemoryItem(r.Context(), id)
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "visibility": store.NormalizeVisibility(item.Visibility)})
			return
		}
	}
	userID := strings.TrimSpace(req.UserID)
	role := strings.TrimSpace(req.Role)
	if userID == "" && role == "" {
		writeError(w, http.StatusBadRequest, "user_id or role is required (or visibility alone)")
		return
	}
	if userID != "" && role != "" {
		writeError(w, http.StatusBadRequest, "provide exactly one of user_id or role")
		return
	}
	sh, err := ss.ShareMemory(r.Context(), id, userID, role, subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not share memory: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sh)
}

func (s *Server) handleMemorySharesList(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	if !s.authorizeMemory(w, r, id) {
		return
	}
	ss, ok := s.sharingStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory sharing not supported by configured store")
		return
	}
	items, err := ss.ListMemoryShares(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list shares: "+err.Error())
		return
	}
	if items == nil {
		items = []*store.MemoryShare{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleMemoryUnshare(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	userID := strings.TrimSpace(r.PathValue("userId"))
	if id == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "memory id and userId path parameters are required")
		return
	}
	item, err := s.Store.GetMemoryItem(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load memory: "+err.Error())
		return
	}
	if !s.authorizeProject(w, r, item.ProjectID) {
		return
	}
	ss, ok := s.sharingStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory sharing not supported by configured store")
		return
	}
	if err := ss.UnshareMemory(r.Context(), id, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "share not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not unshare memory: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memory_id": id, "user_id": userID, "revoked": true})
}

type memoryCopyRequest struct {
	ProjectID string `json:"project_id"`
}

func (s *Server) handleMemoryCopy(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	if !s.authorizeMemory(w, r, id) {
		return
	}
	var req memoryCopyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	target := strings.TrimSpace(req.ProjectID)
	if target == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	// memory:write on target (Phase 3 RBAC): until roles land, membership
	// is the write gate — same as POST /memory.
	if !s.authorizeProject(w, r, target) {
		return
	}
	ss, ok := s.sharingStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory copy not supported by configured store")
		return
	}
	dup, err := ss.CopyMemory(r.Context(), id, target, authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not copy memory: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, dup)
}

func (s *Server) handleMemoryConfirm(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	if !s.authorizeMemoryPermission(w, r, id, store.PermMemoryConfirm) {
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

// rejectMemoryStore is implemented by stores with native rejection
// (MemStore + PostgresStore both implement RejectMemory since Wave 1).
type rejectMemoryStore interface {
	RejectMemory(ctx context.Context, id, rejectedBy string) error
}

func (s *Server) handleMemoryReject(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	// Reject is the practical delete/dismiss path until soft-delete ships.
	if !s.authorizeMemoryPermission(w, r, id, store.PermMemoryDelete) {
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
			if errors.Is(err, store.ErrConflict) {
				writeError(w, http.StatusConflict, err.Error())
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
	writeError(w, http.StatusNotImplemented, "rejection not supported by configured store")
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
	// Object → project authorization (issue #141) before mutation.
	ep, err := s.Store.GetEpisode(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "episode not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load episode: "+err.Error())
		return
	}
	if !s.authorizeProject(w, r, ep.ProjectID) {
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
	ep, err = s.Store.GetEpisode(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "RESOLVED"})
		return
	}
	writeJSON(w, http.StatusOK, ep)
}

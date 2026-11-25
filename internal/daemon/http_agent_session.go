package daemon

// This file mirrors harvested conversations into the current cloud-session
// contract.  HTTPMemoryStore originally only published legacy snapshots;
// keeping this adapter beside it lets older portals continue receiving those
// snapshots while current portals receive real Timeline/Teleport sessions.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"central-memory/internal/store"
	"central-memory/internal/usage"
)

// MirroredTurn is the safe, preview-only part of a harvested turn that is
// useful in the Timeline.  The complete redacted transcript is uploaded as a
// version blob when the session becomes idle.
type MirroredTurn struct {
	Idx         int64
	Role        string
	TextPreview string
	ToolCalls   []map[string]any
	SourceAt    *time.Time
	// Usage is the token usage recorded with this turn. UsageOnly turns
	// (Codex token_count, tool-only Claude lines) are not dialogue: they
	// go to the usage ledger only.
	Usage     *usage.Entry
	UsageKey  string
	UsageOnly bool
}

// usageBody renders the optional usage object of the turn/usage payloads.
func usageBody(u usage.Entry, key string) map[string]any {
	body := map[string]any{"model": u.Model, "input": u.Input, "output": u.Output,
		"cache_read": u.CacheRead, "cache_write": u.CacheWrite}
	if u.CostUSD != nil {
		body["cost_usd"] = *u.CostUSD
	}
	if key != "" {
		body["key"] = key
	}
	return body
}

// mirrorConnection returns a coherent connection snapshot.  The project id
// can change after daemon registration, so do not read these fields directly.
func (s *HTTPMemoryStore) mirrorConnection() (base, token, projectID string, client *http.Client, err error) {
	if s == nil {
		return "", "", "", nil, fmt.Errorf("daemon: cloud session mirror: nil store")
	}
	s.mu.Lock()
	base, token, projectID, client = s.Base, s.Token, s.ProjectID, s.HTTP
	s.mu.Unlock()
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	token = strings.TrimSpace(token)
	projectID = strings.TrimSpace(projectID)
	if base == "" || token == "" || projectID == "" {
		return "", "", "", nil, fmt.Errorf("daemon: cloud session mirror: missing server, token, or project")
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return base, token, projectID, client, nil
}

func mirrorJSON(ctx context.Context, client *http.Client, method, endpoint, token string, body any, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	return mirrorDo(client, req, token, out)
}

// mirrorStream is mirrorJSON for a body too large to build in memory.
func mirrorStream(ctx context.Context, client *http.Client, method, endpoint, token string, length int64, open func() (io.ReadCloser, error), out any) (int, error) {
	body, err := open()
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		body.Close()
		return 0, err
	}
	req.ContentLength = length
	req.GetBody = open
	return mirrorDo(client, req, token, out)
}

// mirrorStatusError is a non-2xx response from the cloud.
type mirrorStatusError struct {
	status int
	msg    string
}

func (e *mirrorStatusError) Error() string { return e.msg }

func mirrorDo(client *http.Client, req *http.Request, token string, out any) (int, error) {
	req.Header.Set("Content-Type", "application/json")
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
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return resp.StatusCode, &mirrorStatusError{status: resp.StatusCode,
			msg: fmt.Sprintf("daemon: cloud session mirror: %s %s", resp.Status, strings.TrimSpace(string(data)))}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("daemon: cloud session mirror: decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// mirroredSession is what the last successful upsert of a session sent.
type mirroredSession struct {
	id, title, parent string
}

// maxMirroredSessions bounds the upsert cache; it is cleared when full.
const maxMirroredSessions = 4096

func mirroredSessionKey(base, projectID, harness, nativeID, machineID, workspaceRoot string) string {
	return strings.Join([]string{base, projectID, harness, nativeID, strings.TrimSpace(machineID), strings.TrimSpace(workspaceRoot)}, "\x00")
}

// forgetMirroredSession drops a cached session (after an upload against it
// failed, the session may be gone on the server; the next batch upserts).
func (s *HTTPMemoryStore) forgetMirroredSession(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	delete(s.mirrored, key)
	s.mu.Unlock()
}

// upsertMirroredSession creates or touches the cloud session with a stable
// (project, harness, native id, machine) identity. parentNativeID links a
// sub-agent run to the session that started it ("" when not one).
//
// The upsert only sets the title and the parent link (the server never
// clears either), so it is sent only when one of them changes: the first
// time a session is seen in this process, or when it gains a new title or
// parent. Every flush used to upsert every session it touched, again for
// each snapshot. The returned key identifies the cache entry.
func (s *HTTPMemoryStore) upsertMirroredSession(ctx context.Context, harness, nativeID, machineID, workspaceRoot, title, parentNativeID string) (id, key string, err error) {
	base, token, projectID, client, err := s.mirrorConnection()
	if err != nil {
		return "", "", err
	}
	harness = normalizeHarness(harness)
	nativeID = strings.TrimSpace(nativeID)
	if harness == "" || nativeID == "" {
		return "", "", fmt.Errorf("daemon: cloud session mirror: harness and native id are required")
	}
	title = strings.TrimSpace(title)
	parentNativeID = strings.TrimSpace(parentNativeID)
	key = mirroredSessionKey(base, projectID, harness, nativeID, machineID, workspaceRoot)
	s.mu.Lock()
	cached, known := s.mirrored[key]
	s.mu.Unlock()
	if known && (title == "" || title == cached.title) && (parentNativeID == "" || parentNativeID == cached.parent) {
		return cached.id, key, nil
	}
	var out struct {
		ID string `json:"id"`
	}
	body := map[string]any{
		"project_id":          projectID,
		"harness":             harness,
		"native_id":           nativeID,
		"origin_machine_id":   strings.TrimSpace(machineID),
		"workspace_root_hint": strings.TrimSpace(workspaceRoot),
		"title":               title,
		"visibility":          "private",
	}
	if parentNativeID != "" {
		body["parent_native_id"] = parentNativeID
	}
	status, err := mirrorJSON(ctx, client, http.MethodPost, base+"/v1/agent-sessions", token, body, &out)
	if err != nil && status == http.StatusBadRequest && parentNativeID != "" {
		// Servers before parent_native_id reject unknown fields: the run
		// is still captured, only without its parent link.
		delete(body, "parent_native_id")
		_, err = mirrorJSON(ctx, client, http.MethodPost, base+"/v1/agent-sessions", token, body, &out)
	}
	if err != nil {
		return "", key, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return "", key, fmt.Errorf("daemon: cloud session mirror: server returned no session id")
	}
	next := mirroredSession{id: out.ID, title: cached.title, parent: cached.parent}
	if title != "" {
		next.title = title
	}
	if parentNativeID != "" {
		next.parent = parentNativeID
	}
	s.mu.Lock()
	if s.mirrored == nil || len(s.mirrored) >= maxMirroredSessions {
		s.mirrored = map[string]mirroredSession{}
	}
	s.mirrored[key] = next
	s.mu.Unlock()
	return out.ID, key, nil
}

// MirrorAgentSessionTurns makes a captured conversation visible in Timeline
// immediately.  Indexes are deterministic source positions: a retry that
// reaches an already-written turn receives 409 and is therefore idempotent.
func (s *HTTPMemoryStore) MirrorAgentSessionTurns(ctx context.Context, harness, nativeID, machineID, workspaceRoot string, turns []MirroredTurn, title ...string) (string, error) {
	name := ""
	if len(title) > 0 {
		name = title[0]
	}
	return s.MirrorAgentSessionTurnsWithParent(ctx, harness, nativeID, machineID, workspaceRoot, "", turns, name)
}

// MirrorAgentSessionTurnsWithParent is MirrorAgentSessionTurns for a session
// that may be a sub-agent run: parentNativeID is the native id of the
// session that started it, or "".
func (s *HTTPMemoryStore) MirrorAgentSessionTurnsWithParent(ctx context.Context, harness, nativeID, machineID, workspaceRoot, parentNativeID string, turns []MirroredTurn, title string) (sessionID string, err error) {
	// No title in this batch: send none, so the server keeps the name it has
	// (a placeholder here overwrote a real title on every later batch). The
	// apps name untitled chats from their first message.
	sessionID, key, err := s.upsertMirroredSession(ctx, harness, nativeID, machineID, workspaceRoot, title, parentNativeID)
	if err != nil || len(turns) == 0 {
		return sessionID, err
	}
	defer func() {
		if err != nil {
			s.forgetMirroredSession(key)
		}
	}()
	base, token, _, client, err := s.mirrorConnection()
	if err != nil {
		return sessionID, err
	}
	turns = s.postUsage(ctx, client, base, token, sessionID, turns)
	if len(turns) == 0 {
		return sessionID, nil
	}
	// The live database may contain thousands of historical turns. Upload in
	// bounded chunks; old API builds return 404 and use the single-turn route.
	fallback := false
	for start := 0; start < len(turns); start += 50 {
		end := start + 50
		if end > len(turns) {
			end = len(turns)
		}
		batch := make([]map[string]any, 0, end-start)
		for _, turn := range turns[start:end] {
			role := strings.TrimSpace(turn.Role)
			if role == "" || role == "unknown" {
				role = "assistant"
			}
			calls := turn.ToolCalls
			if calls == nil {
				calls = []map[string]any{}
			}
			body := map[string]any{
				"idx": turn.Idx, "role": role,
				"text_preview": turn.TextPreview, "tool_calls": calls,
			}
			if turn.SourceAt != nil {
				body["source_ts"] = turn.SourceAt
			}
			batch = append(batch, body)
		}
		status, err := mirrorJSON(ctx, client, http.MethodPost,
			base+"/v1/agent-sessions/"+url.PathEscape(sessionID)+"/turns/batch",
			token, map[string]any{"turns": batch}, nil)
		if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
			fallback = true
			break
		}
		if err != nil {
			return sessionID, err
		}
	}
	if !fallback {
		return sessionID, nil
	}
	for _, turn := range turns {
		role := strings.TrimSpace(turn.Role)
		if role == "" || role == "unknown" {
			role = "assistant"
		}
		calls := turn.ToolCalls
		if calls == nil {
			calls = []map[string]any{}
		}
		body := map[string]any{
			"idx":          turn.Idx,
			"role":         role,
			"text_preview": turn.TextPreview,
			"tool_calls":   calls,
		}
		if turn.SourceAt != nil {
			body["source_ts"] = turn.SourceAt
		}
		status, err := mirrorJSON(ctx, client, http.MethodPost,
			base+"/v1/agent-sessions/"+url.PathEscape(sessionID)+"/turns", token, body, nil)
		if err != nil && status != http.StatusConflict {
			return sessionID, err
		}
	}
	return sessionID, nil
}

// postUsage sends every turn's usage to the usage ledger and returns the
// dialogue turns. Usage travels on its own route rather than inside turn
// bodies because servers decode turns strictly: an older server would
// reject the whole turn. Best effort: a server without the route (404/405)
// or a failed post never blocks the dialogue upload.
func (s *HTTPMemoryStore) postUsage(ctx context.Context, client *http.Client, base, token, sessionID string, turns []MirroredTurn) []MirroredTurn {
	dialogue := turns[:0:0]
	var records []map[string]any
	for _, turn := range turns {
		if !turn.UsageOnly {
			dialogue = append(dialogue, turn)
		}
		if turn.Usage == nil {
			continue
		}
		record := usageBody(*turn.Usage, turn.UsageKey)
		record["idx"] = turn.Idx
		if turn.SourceAt != nil {
			record["source_ts"] = turn.SourceAt
		}
		records = append(records, record)
	}
	for start := 0; start < len(records); start += 200 {
		end := min(start+200, len(records))
		_, err := mirrorJSON(ctx, client, http.MethodPost,
			base+"/v1/agent-sessions/"+url.PathEscape(sessionID)+"/usage",
			token, map[string]any{"records": records[start:end]}, nil)
		if err != nil {
			break
		}
	}
	return dialogue
}

// PublishAgentSessionVersion commits a transcript-only version.  It uses the
// existing redacted snapshot payload but the modern blob/version endpoints,
// so Teleport and restore readiness are based on a complete, immutable
// version rather than an opaque legacy snapshot.
func (s *HTTPMemoryStore) PublishAgentSessionVersion(ctx context.Context, snap *store.SessionSnapshot, workspaceRoot string) (err error) {
	if snap == nil {
		return fmt.Errorf("daemon: cloud session mirror: nil snapshot")
	}
	sessionID, key, err := s.upsertMirroredSession(ctx, snap.Harness, snap.ConversationID, snap.SourceMachineID, workspaceRoot, "", "")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.forgetMirroredSession(key)
		}
	}()
	// Hash and upload stream from the gzip payload: the decompressed
	// transcript is never held in memory (snapshot_stream.go).
	hash, size, err := gzipDigest(snap.TranscriptPayload)
	if err != nil {
		return fmt.Errorf("daemon: cloud session mirror: decode transcript: %w", err)
	}
	if size == 0 {
		return fmt.Errorf("daemon: cloud session mirror: empty transcript")
	}
	base, token, projectID, client, err := s.mirrorConnection()
	if err != nil {
		return err
	}
	if err := s.uploadTranscriptBlob(ctx, client, base, token, projectID, snap.TranscriptPayload, hash, size); err != nil {
		return err
	}
	var version struct {
		Version int `json:"version"`
	}
	if _, err := mirrorJSON(ctx, client, http.MethodPost,
		base+"/v1/agent-sessions/"+url.PathEscape(sessionID)+"/versions", token, map[string]any{}, &version); err != nil {
		return err
	}
	if version.Version < 1 {
		return fmt.Errorf("daemon: cloud session mirror: server returned invalid version")
	}
	_, err = mirrorJSON(ctx, client, http.MethodPost,
		base+"/v1/agent-sessions/"+url.PathEscape(sessionID)+"/versions/"+fmt.Sprint(version.Version)+"/complete", token, map[string]any{
			"harness":    normalizeHarness(snap.Harness),
			"native_id":  strings.TrimSpace(snap.ConversationID),
			"transcript": map[string]string{"blob": "sha256:" + hash},
		}, nil)
	return err
}

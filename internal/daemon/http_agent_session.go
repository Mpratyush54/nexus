package daemon

// This file mirrors harvested conversations into the current cloud-session
// contract.  HTTPMemoryStore originally only published legacy snapshots;
// keeping this adapter beside it lets older portals continue receiving those
// snapshots while current portals receive real Timeline/Teleport sessions.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"central-memory/internal/store"
)

// MirroredTurn is the safe, preview-only part of a harvested turn that is
// useful in the Timeline.  The complete redacted transcript is uploaded as a
// version blob when the session becomes idle.
type MirroredTurn struct {
	Idx         int64
	Role        string
	TextPreview string
	ToolCalls   []map[string]any
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
		return resp.StatusCode, fmt.Errorf("daemon: cloud session mirror: %s %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("daemon: cloud session mirror: decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// upsertMirroredSession creates or touches the cloud session with a stable
// (project, harness, native id, machine) identity.
func (s *HTTPMemoryStore) upsertMirroredSession(ctx context.Context, harness, nativeID, machineID, workspaceRoot string) (string, error) {
	base, token, projectID, client, err := s.mirrorConnection()
	if err != nil {
		return "", err
	}
	harness = normalizeHarness(harness)
	nativeID = strings.TrimSpace(nativeID)
	if harness == "" || nativeID == "" {
		return "", fmt.Errorf("daemon: cloud session mirror: harness and native id are required")
	}
	var out struct {
		ID string `json:"id"`
	}
	_, err = mirrorJSON(ctx, client, http.MethodPost, base+"/v1/agent-sessions", token, map[string]any{
		"project_id":          projectID,
		"harness":             harness,
		"native_id":           nativeID,
		"origin_machine_id":   strings.TrimSpace(machineID),
		"workspace_root_hint": strings.TrimSpace(workspaceRoot),
		"title":               "Captured " + harness + " session",
		"visibility":          "private",
	}, &out)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out.ID) == "" {
		return "", fmt.Errorf("daemon: cloud session mirror: server returned no session id")
	}
	return out.ID, nil
}

// MirrorAgentSessionTurns makes a captured conversation visible in Timeline
// immediately.  Indexes are deterministic source positions: a retry that
// reaches an already-written turn receives 409 and is therefore idempotent.
func (s *HTTPMemoryStore) MirrorAgentSessionTurns(ctx context.Context, harness, nativeID, machineID, workspaceRoot string, turns []MirroredTurn) (string, error) {
	sessionID, err := s.upsertMirroredSession(ctx, harness, nativeID, machineID, workspaceRoot)
	if err != nil || len(turns) == 0 {
		return sessionID, err
	}
	base, token, _, client, err := s.mirrorConnection()
	if err != nil {
		return sessionID, err
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
		status, err := mirrorJSON(ctx, client, http.MethodPost,
			base+"/v1/agent-sessions/"+url.PathEscape(sessionID)+"/turns", token, map[string]any{
				"idx":          turn.Idx,
				"role":         role,
				"text_preview": turn.TextPreview,
				"tool_calls":   calls,
			}, nil)
		if err != nil && status != http.StatusConflict {
			return sessionID, err
		}
	}
	return sessionID, nil
}

// PublishAgentSessionVersion commits a transcript-only version.  It uses the
// existing redacted snapshot payload but the modern blob/version endpoints,
// so Teleport and restore readiness are based on a complete, immutable
// version rather than an opaque legacy snapshot.
func (s *HTTPMemoryStore) PublishAgentSessionVersion(ctx context.Context, snap *store.SessionSnapshot, workspaceRoot string) error {
	if snap == nil {
		return fmt.Errorf("daemon: cloud session mirror: nil snapshot")
	}
	sessionID, err := s.upsertMirroredSession(ctx, snap.Harness, snap.ConversationID, snap.SourceMachineID, workspaceRoot)
	if err != nil {
		return err
	}
	transcript, err := ungzipBytes(snap.TranscriptPayload)
	if err != nil {
		return fmt.Errorf("daemon: cloud session mirror: decode transcript: %w", err)
	}
	if len(transcript) == 0 {
		return fmt.Errorf("daemon: cloud session mirror: empty transcript")
	}
	base, token, projectID, client, err := s.mirrorConnection()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(transcript)
	hash := hex.EncodeToString(sum[:])
	if _, err := mirrorJSON(ctx, client, http.MethodPut,
		base+"/v1/blobs/"+hash, token, map[string]any{
			"project_id": projectID,
			"body_b64":   base64.StdEncoding.EncodeToString(transcript),
			"kind":       "plain",
			"purpose":    "transcript",
		}, nil); err != nil {
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

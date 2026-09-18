// remotestore.go — MCP Store backed by the Central Server REST API (issue #166).
//
// When ServerURL (+ token) is set, cmd/mem mcp uses this instead of the local
// MemStore so searches/writes hit the shared project and humans editing in
// the PWA are visible to agents on the next tool call (no local cache).

package mcp

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
)

// HTTPStore implements Store against the central server HTTP API.
type HTTPStore struct {
	Base   string
	Token  string
	HTTP   *http.Client
	Client *http.Client // alias; HTTP preferred
}

// NewHTTPStore builds a remote store. base is the server root URL.
func NewHTTPStore(base, token string) *HTTPStore {
	return &HTTPStore{
		Base:  strings.TrimSuffix(strings.TrimSpace(base), "/"),
		Token: strings.TrimSpace(token),
		HTTP:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *HTTPStore) client() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

func (h *HTTPStore) do(ctx context.Context, method, path string, query url.Values, body any) ([]byte, int, error) {
	u := h.Base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return data, resp.StatusCode, fmt.Errorf("server %s %s: %s", method, path, strings.TrimSpace(string(data)))
	}
	return data, resp.StatusCode, nil
}

func (h *HTTPStore) SearchMemory(ctx context.Context, projectID, query string, tags []string, limit int) ([]*MemoryItem, error) {
	q := url.Values{}
	q.Set("project_id", projectID)
	q.Set("q", query)
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if len(tags) > 0 {
		q.Set("tags", strings.Join(tags, ","))
	}
	data, _, err := h.do(ctx, http.MethodGet, "/memory/search", q, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []*MemoryItem `json:"items"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (h *HTTPStore) CreateMemoryItem(ctx context.Context, item *MemoryItem) error {
	if item == nil {
		return fmt.Errorf("mcp: memory item is required")
	}
	body := map[string]any{
		"project_id":      item.ProjectID,
		"key":             item.Key,
		"content":         item.Content,
		"context_snippet": item.ContextSnippet,
		"level":           item.Level,
		"scope":           item.Scope,
		"tags":            item.Tags,
		"user_id":         item.UserID,
		"session_id":      item.SessionID,
		"source":          item.Source,
	}
	data, _, err := h.do(ctx, http.MethodPost, "/memory", nil, body)
	if err != nil {
		return err
	}
	var created MemoryItem
	if err := json.Unmarshal(data, &created); err != nil {
		return err
	}
	if created.ID != "" {
		item.ID = created.ID
	}
	if created.Status != "" {
		item.Status = created.Status
	}
	return nil
}

func (h *HTTPStore) SearchEpisodes(ctx context.Context, projectID, errorPattern, query string, limit int) ([]*Episode, error) {
	q := url.Values{}
	q.Set("project_id", projectID)
	if query != "" {
		q.Set("q", query)
	}
	if errorPattern != "" {
		q.Set("error_pattern", errorPattern)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	data, _, err := h.do(ctx, http.MethodGet, "/episodes/search", q, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []*Episode `json:"items"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (h *HTTPStore) CreateEpisode(ctx context.Context, ep *Episode) error {
	if ep == nil {
		return fmt.Errorf("mcp: episode is required")
	}
	data, _, err := h.do(ctx, http.MethodPost, "/episodes", nil, ep)
	if err != nil {
		return err
	}
	var created Episode
	if err := json.Unmarshal(data, &created); err != nil {
		return err
	}
	if created.ID != "" {
		ep.ID = created.ID
	}
	if created.Status != "" {
		ep.Status = created.Status
	}
	return nil
}

func (h *HTTPStore) GetActiveWorkspace(ctx context.Context, projectID string) (*Workspace, error) {
	data, code, err := h.do(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(projectID)+"/active", nil, nil)
	if err != nil {
		if code == http.StatusNotFound {
			return &Workspace{}, nil
		}
		return nil, err
	}
	var ws Workspace
	if err := json.Unmarshal(data, &ws); err != nil {
		return nil, err
	}
	return &ws, nil
}

func (h *HTTPStore) GetProject(ctx context.Context, id string) (*Project, error) {
	// No dedicated GET /projects/{id}; resolve via folder is lossy. Return a
	// stub so workspace_info still works when remote.
	_ = ctx
	return &Project{DisplayName: id, FolderName: id}, nil
}

// LogToolCall POSTs one MCP_TOOL_CALL to the server for WS fan-out.
func (h *HTTPStore) LogToolCall(ctx context.Context, projectID string, call ToolCallLog) error {
	body := map[string]any{
		"agent_id":    call.AgentID,
		"tool_name":   call.ToolName,
		"arguments":   call.Arguments,
		"result":      call.Result,
		"error":       call.Error,
		"duration_ms": call.DurationMs,
		"session_id":  call.SessionID,
	}
	_, _, err := h.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(projectID)+"/mcp/tool-calls", nil, body)
	return err
}

// FetchAgentAccess loads one agent's permission config (404 → nil, nil).
func (h *HTTPStore) FetchAgentAccess(ctx context.Context, projectID, agentID string) (*AgentAccess, error) {
	data, code, err := h.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(projectID)+"/agents/"+url.PathEscape(agentID), nil, nil)
	if err != nil {
		if code == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	var perm struct {
		AgentID   string          `json:"agent_id"`
		Mode      string          `json:"mode"`
		RateLimit int             `json:"rate_limit"`
		Tools     map[string]bool `json:"tools"`
	}
	if err := json.Unmarshal(data, &perm); err != nil {
		return nil, err
	}
	return &AgentAccess{
		AgentID:   perm.AgentID,
		Mode:      perm.Mode,
		RateLimit: perm.RateLimit,
		Tools:     perm.Tools,
	}, nil
}

// ResolveProject POSTs /projects/resolve and returns the canonical project id + display name.
func (h *HTTPStore) ResolveProject(ctx context.Context, canonicalURL, rootCommit, folderName string) (id, displayName string, err error) {
	body := map[string]string{
		"canonical_url": canonicalURL,
		"root_commit":   rootCommit,
		"folder_name":   folderName,
	}
	data, _, err := h.do(ctx, http.MethodPost, "/projects/resolve", nil, body)
	if err != nil {
		return "", "", err
	}
	var p struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		FolderName  string `json:"folder_name"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return "", "", err
	}
	name := p.DisplayName
	if name == "" {
		name = p.FolderName
	}
	return p.ID, name, nil
}

// ToolCallLog is one MCP tool invocation summary for logging.
type ToolCallLog struct {
	AgentID    string
	ToolName   string
	Arguments  map[string]any
	Result     map[string]any
	Error      string
	DurationMs int64
	SessionID  string
}

// ToolCallLogger receives completed tool calls (server POST or interceptor).
type ToolCallLogger interface {
	LogToolCall(ctx context.Context, projectID string, call ToolCallLog) error
}

var _ Store = (*HTTPStore)(nil)
var _ ToolCallLogger = (*HTTPStore)(nil)

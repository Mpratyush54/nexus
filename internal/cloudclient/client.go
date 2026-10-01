// Package cloudclient calls Nexus cloud HTTP APIs from the native desktop shell
// (Bearer token from nexus login / config.json).
package cloudclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"central-memory/internal/config"
)

// Client talks to the central API (default api-nexus.pratyushes.dev).
type Client struct {
	ServerURL string
	Token     string
	HTTP      *http.Client
}

// New builds a client from config when serverURL/token are empty.
func New(serverURL, token string) *Client {
	if strings.TrimSpace(serverURL) == "" {
		serverURL = config.ResolveServerURL("https://api-nexus.pratyushes.dev")
	}
	if strings.TrimSpace(token) == "" {
		token = config.ResolveToken()
	}
	return &Client{
		ServerURL: strings.TrimRight(strings.TrimSpace(serverURL), "/"),
		Token:     strings.TrimSpace(token),
		HTTP:      &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) signedIn() bool {
	return c != nil && strings.TrimSpace(c.Token) != ""
}

// SignedIn reports whether a Bearer token is configured.
func (c *Client) SignedIn() bool { return c.signedIn() }

// Do implements core.Cloud: one HTTPS round-trip to ServerURL+path.
func (c *Client) Do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if !c.signedIn() {
		return nil, fmt.Errorf("not signed in — open Settings and sign in")
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodGet
	}
	path = strings.TrimSpace(path)
	if path == "" || !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("cloudclient: path must start with /")
	}
	var rdr io.Reader
	if len(body) > 0 && method != http.MethodGet && method != http.MethodHead {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.ServerURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("session expired — open Settings and sign in again")
		}
		return nil, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, msg)
	}
	return raw, nil
}

// MemoryItem is one search hit from POST /v1/agent/memory/search.
type MemoryItem struct {
	ID         string   `json:"id"`
	Key        string   `json:"key"`
	Content    string   `json:"content"`
	Level      string   `json:"level"`
	Scope      string   `json:"scope"`
	Status     string   `json:"status"`
	Confidence float64  `json:"confidence"`
	Tags       []string `json:"tags"`
	Source     string   `json:"source"`
}

type memorySearchResp struct {
	ProjectID string       `json:"project_id"`
	Count     int          `json:"count"`
	Items     []MemoryItem `json:"items"`
}

// MemorySearch runs semantic memory search for the signed-in user.
func (c *Client) MemorySearch(query, projectID string, limit int) ([]MemoryItem, string, error) {
	if !c.signedIn() {
		return nil, "", fmt.Errorf("not signed in")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, "", fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	body, _ := json.Marshal(map[string]any{
		"query":      query,
		"project_id": strings.TrimSpace(projectID),
		"limit":      limit,
	})
	req, err := http.NewRequest(http.MethodPost, c.ServerURL+"/v1/agent/memory/search", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("memory search: %s %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out memorySearchResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", err
	}
	return out.Items, out.ProjectID, nil
}

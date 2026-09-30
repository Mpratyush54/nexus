// Package cloudclient calls Nexus cloud HTTP APIs from the native desktop shell
// (Bearer token from nexus login / config.json).
package cloudclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// Project is one entry from GET /v1/agent/projects.
type Project struct {
	ID          string `json:"id"`
	FolderName  string `json:"folder_name"`
	DisplayName string `json:"display_name"`
}

// MemoryItem is one search/browse hit (agent or portal shape).
type MemoryItem struct {
	ID             string   `json:"id"`
	Key            string   `json:"key"`
	Content        string   `json:"content"`
	Level          string   `json:"level"`
	Scope          string   `json:"scope"`
	Status         string   `json:"status"`
	Confidence     float64  `json:"confidence"`
	Tags           []string `json:"tags"`
	Source         string   `json:"source"`
	ProjectID      string   `json:"project_id"`
	Category       string   `json:"category"`
	ContextSnippet string   `json:"context_snippet"`
	FilesAffected  []string `json:"files_affected"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	LastUsedAt     string   `json:"last_used_at"`
}

type memorySearchResp struct {
	ProjectID string       `json:"project_id"`
	Count     int          `json:"count"`
	Items     []MemoryItem `json:"items"`
}

type projectListResp struct {
	Items []Project `json:"items"`
	Count int       `json:"count"`
}

// ListProjects returns projects visible to the signed-in token.
func (c *Client) ListProjects() ([]Project, error) {
	if !c.signedIn() {
		return nil, fmt.Errorf("not signed in")
	}
	req, err := http.NewRequest(http.MethodGet, c.ServerURL+"/v1/agent/projects", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list projects: %s %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out projectListResp
	if err := json.Unmarshal(raw, &out); err != nil {
		// Some handlers return a bare array.
		var items []Project
		if err2 := json.Unmarshal(raw, &items); err2 != nil {
			return nil, err
		}
		return items, nil
	}
	return out.Items, nil
}

// MemoryBrowse lists recent memories via portal GET /memory/search (empty q allowed).
func (c *Client) MemoryBrowse(projectID string, limit int) ([]MemoryItem, string, error) {
	return c.memoryPortalSearch(projectID, "", limit)
}

// memoryPortalSearch uses the same GET /memory/search the web Memory page uses
// (empty q = browse recent; returns full item fields including timestamps/files).
func (c *Client) memoryPortalSearch(projectID, query string, limit int) ([]MemoryItem, string, error) {
	if !c.signedIn() {
		return nil, "", fmt.Errorf("not signed in")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, "", fmt.Errorf("project_id is required to browse memory")
	}
	if limit <= 0 {
		limit = 40
	}
	q := url.Values{}
	q.Set("project_id", projectID)
	q.Set("limit", fmt.Sprintf("%d", limit))
	if query = strings.TrimSpace(query); query != "" {
		q.Set("q", query)
	}
	req, err := http.NewRequest(http.MethodGet, c.ServerURL+"/memory/search?"+q.Encode(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("memory search: %s %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out memorySearchResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", err
	}
	pid := out.ProjectID
	if pid == "" {
		pid = projectID
	}
	for i := range out.Items {
		if out.Items[i].ProjectID == "" {
			out.Items[i].ProjectID = pid
		}
		out.Items[i].CreatedAt = normalizeTime(out.Items[i].CreatedAt)
		out.Items[i].UpdatedAt = normalizeTime(out.Items[i].UpdatedAt)
		out.Items[i].LastUsedAt = normalizeTime(out.Items[i].LastUsedAt)
	}
	return out.Items, pid, nil
}

// MemorySearch loads memories for the signed-in user.
// With projectID: portal GET /memory/search (empty query = recent browse, full fields).
// Without projectID: agent POST (auto-resolves single-project tokens; query required).
func (c *Client) MemorySearch(query, projectID string, limit int) ([]MemoryItem, string, error) {
	if !c.signedIn() {
		return nil, "", fmt.Errorf("not signed in")
	}
	query = strings.TrimSpace(query)
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		return c.memoryPortalSearch(projectID, query, limit)
	}
	if query == "" {
		return nil, "", fmt.Errorf("query is required when project_id is unknown")
	}
	if limit <= 0 {
		limit = 20
	}
	body, _ := json.Marshal(map[string]any{
		"query": query,
		"limit": limit,
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
	for i := range out.Items {
		if out.Items[i].ProjectID == "" {
			out.Items[i].ProjectID = out.ProjectID
		}
	}
	return out.Items, out.ProjectID, nil
}

// normalizeTime accepts RFC3339 JSON strings (or already-normalized).
func normalizeTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "0001-01-01T00:00:00Z" {
		return ""
	}
	return s
}

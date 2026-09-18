// Package github is a thin GitHub REST client for collaborator import (issue #167).
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Collaborator is a GitHub repo collaborator with permission level.
type Collaborator struct {
	Login       string
	ID          int64
	Email       string
	Permissions string // admin | write | read | maintain | triage
}

// Client talks to api.github.com.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	Token   string
}

// NewClient builds a client with a 20s timeout. Token may be empty for public repos.
func NewClient(token string) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		BaseURL: "https://api.github.com",
		Token:   strings.TrimSpace(token),
	}
}

// ListCollaborators pages through GET /repos/{owner}/{repo}/collaborators.
func (c *Client) ListCollaborators(ctx context.Context, owner, repo string) ([]Collaborator, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("github: owner and repo are required")
	}
	var out []Collaborator
	page := 1
	for {
		url := fmt.Sprintf("%s/repos/%s/%s/collaborators?per_page=100&page=%d",
			strings.TrimRight(c.BaseURL, "/"), owner, repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github: list collaborators: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("github: repository %s/%s not found or inaccessible", owner, repo)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("github: auth failed (%d) — provide a valid access token", resp.StatusCode)
		}
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("github: list collaborators: %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		var raw []struct {
			Login       string `json:"login"`
			ID          int64  `json:"id"`
			Email       string `json:"email"`
			Permissions struct {
				Admin    bool `json:"admin"`
				Maintain bool `json:"maintain"`
				Push     bool `json:"push"`
				Triage   bool `json:"triage"`
				Pull     bool `json:"pull"`
			} `json:"permissions"`
			RoleName string `json:"role_name"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("github: decode collaborators: %w", err)
		}
		if len(raw) == 0 {
			break
		}
		for _, row := range raw {
			perm := mapGitHubPerm(row.Permissions.Admin, row.Permissions.Maintain, row.Permissions.Push, row.Permissions.Triage, row.Permissions.Pull, row.RoleName)
			out = append(out, Collaborator{
				Login:       row.Login,
				ID:          row.ID,
				Email:       strings.TrimSpace(row.Email),
				Permissions: perm,
			})
		}
		if len(raw) < 100 {
			break
		}
		page++
		if page > 20 {
			break
		}
	}
	return out, nil
}

func mapGitHubPerm(admin, maintain, push, triage, pull bool, roleName string) string {
	role := strings.ToLower(strings.TrimSpace(roleName))
	switch role {
	case "admin":
		return "admin"
	case "maintain":
		return "maintain"
	case "write":
		return "write"
	case "triage":
		return "triage"
	case "read":
		return "read"
	}
	switch {
	case admin:
		return "admin"
	case maintain:
		return "maintain"
	case push:
		return "write"
	case triage:
		return "triage"
	case pull:
		return "read"
	default:
		return "read"
	}
}

// ParseOwnerRepo extracts owner/repo from a GitHub URL, SSH remote, or
// "owner/repo" shorthand. Returns empty strings when nothing GitHub-shaped.
func ParseOwnerRepo(raw string) (owner, repo string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ""
	}
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimRight(s, "/")
	lower := strings.ToLower(s)
	if i := strings.Index(lower, "github.com"); i >= 0 {
		rest := s[i+len("github.com"):]
		rest = strings.TrimLeft(rest, "/:")
		if at := strings.Index(rest, "@"); at >= 0 && (strings.Index(rest, "/") == -1 || at < strings.Index(rest, "/")) {
			rest = rest[at+1:]
		}
		parts := strings.Split(rest, "/")
		if len(parts) >= 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
		return "", ""
	}
	// scp-like git@github.com:owner/repo already handled above; "owner/repo".
	if strings.Contains(s, "://") || strings.Contains(s, "@") {
		return "", ""
	}
	parts := strings.Split(s, "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1]
	}
	return "", ""
}

// MapPermissionToRole converts GitHub permission → Central Memory built-in role.
// admin/maintain → ADMIN, write/triage → EDITOR, read → VIEWER.
func MapPermissionToRole(perm string) string {
	switch strings.ToLower(strings.TrimSpace(perm)) {
	case "admin", "maintain":
		return "ADMIN"
	case "write", "push", "triage":
		return "EDITOR"
	default:
		return "VIEWER"
	}
}

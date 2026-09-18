package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Repo is a repository the authenticated user can access.
type Repo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Owner         string `json:"owner"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Description   string `json:"description,omitempty"`
}

// OAuthConfig holds GitHub App / OAuth App credentials.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       string
}

// Configured reports whether OAuth can start.
func (c OAuthConfig) Configured() bool {
	return strings.TrimSpace(c.ClientID) != "" && strings.TrimSpace(c.ClientSecret) != "" && strings.TrimSpace(c.RedirectURL) != ""
}

// AuthorizeURL builds the GitHub authorize redirect.
func (c OAuthConfig) AuthorizeURL(state string) string {
	scopes := strings.TrimSpace(c.Scopes)
	if scopes == "" {
		scopes = "read:user repo"
	}
	q := url.Values{}
	q.Set("client_id", strings.TrimSpace(c.ClientID))
	q.Set("redirect_uri", strings.TrimSpace(c.RedirectURL))
	q.Set("scope", scopes)
	q.Set("state", state)
	return "https://github.com/login/oauth/authorize?" + q.Encode()
}

// ExchangeCode trades an OAuth code for an access token.
func (c OAuthConfig) ExchangeCode(ctx context.Context, code string) (token string, err error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", fmt.Errorf("github: oauth code is required")
	}
	form := url.Values{}
	form.Set("client_id", strings.TrimSpace(c.ClientID))
	form.Set("client_secret", strings.TrimSpace(c.ClientSecret))
	form.Set("code", code)
	form.Set("redirect_uri", strings.TrimSpace(c.RedirectURL))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("github: oauth exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("github: oauth decode: %w", err)
	}
	if out.AccessToken == "" {
		msg := out.ErrorDesc
		if msg == "" {
			msg = out.Error
		}
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return "", fmt.Errorf("github: oauth failed: %s", msg)
	}
	return out.AccessToken, nil
}

// ListUserRepos pages through GET /user/repos for the token owner.
func (c *Client) ListUserRepos(ctx context.Context, perPage, maxPages int) ([]Repo, error) {
	if strings.TrimSpace(c.Token) == "" {
		return nil, fmt.Errorf("github: access token required to list repositories")
	}
	if perPage <= 0 || perPage > 100 {
		perPage = 50
	}
	if maxPages <= 0 {
		maxPages = 4
	}
	var out []Repo
	for page := 1; page <= maxPages; page++ {
		u := fmt.Sprintf("%s/user/repos?per_page=%d&page=%d&sort=updated&affiliation=owner,collaborator,organization_member",
			strings.TrimRight(c.BaseURL, "/"), perPage, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("Authorization", "Bearer "+c.Token)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github: list repos: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("github: auth failed (%d)", resp.StatusCode)
		}
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("github: list repos: %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		var raw []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Private  bool   `json:"private"`
			HTMLURL  string `json:"html_url"`
			Default  string `json:"default_branch"`
			Desc     string `json:"description"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("github: decode repos: %w", err)
		}
		if len(raw) == 0 {
			break
		}
		for _, row := range raw {
			out = append(out, Repo{
				ID:            row.ID,
				FullName:      row.FullName,
				Name:          row.Name,
				Owner:         row.Owner.Login,
				Private:       row.Private,
				HTMLURL:       row.HTMLURL,
				DefaultBranch: row.Default,
				Description:   row.Desc,
			})
		}
		if len(raw) < perPage {
			break
		}
	}
	return out, nil
}

package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"central-memory/internal/github"
	"central-memory/internal/store"
)

type oauthPending struct {
	UserID    string
	ProjectID string
	Expires   time.Time
}

type oauthToken struct {
	Token   string
	Expires time.Time
}

var (
	ghOAuthMu      sync.Mutex
	ghOAuthPending = map[string]oauthPending{}
	ghOAuthTokens  = map[string]oauthToken{} // userID → token
)

func (s *Server) githubOAuthConfig() github.OAuthConfig {
	redirect := strings.TrimSpace(os.Getenv("GITHUB_OAUTH_REDIRECT"))
	if redirect == "" {
		base := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_API_URL")), "/")
		if base == "" {
			base = strings.TrimRight(strings.TrimSpace(os.Getenv("API_PUBLIC_URL")), "/")
		}
		if base != "" {
			redirect = base + "/auth/github/callback"
		}
	}
	return github.OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv("GITHUB_CLIENT_ID")),
		ClientSecret: strings.TrimSpace(os.Getenv("GITHUB_CLIENT_SECRET")),
		RedirectURL:  redirect,
		Scopes:       "read:user repo",
	}
}

func appPublicURL() string {
	u := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_APP_URL")), "/")
	if u == "" {
		u = strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_URL")), "/")
	}
	if u == "" {
		u = "http://localhost:5173"
	}
	return u
}

func randomState() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) registerGitHubOAuthRoutes() {
	s.Mux.HandleFunc("GET /projects/{id}/github/oauth/start", s.requireAuth(s.handleGitHubOAuthStart))
	s.Mux.HandleFunc("GET /auth/github/callback", s.handleGitHubOAuthCallback)
	s.Mux.HandleFunc("GET /projects/{id}/github/repos", s.requireAuth(s.handleGitHubRepos))
	s.Mux.HandleFunc("GET /projects/{id}/github/oauth/status", s.requireAuth(s.handleGitHubOAuthStatus))
}

func (s *Server) handleGitHubOAuthStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	cfg := s.githubOAuthConfig()
	uid := authSubject(r)
	ghOAuthMu.Lock()
	tok, ok := ghOAuthTokens[uid]
	live := ok && time.Now().Before(tok.Expires) && tok.Token != ""
	ghOAuthMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"oauth_configured": cfg.Configured(),
		"linked":           live,
		"project_id":       id,
	})
}

func (s *Server) handleGitHubOAuthStart(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizePermission(w, r, id, store.PermMemberManage) {
		return
	}
	cfg := s.githubOAuthConfig()
	if !cfg.Configured() {
		writeError(w, http.StatusServiceUnavailable, "GitHub OAuth is not configured (set GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET, GITHUB_OAUTH_REDIRECT)")
		return
	}
	state := randomState()
	ghOAuthMu.Lock()
	ghOAuthPending[state] = oauthPending{
		UserID:    authSubject(r),
		ProjectID: id,
		Expires:   time.Now().Add(15 * time.Minute),
	}
	ghOAuthMu.Unlock()

	accept := r.Header.Get("Accept")
	authURL := cfg.AuthorizeURL(state)
	if strings.Contains(accept, "application/json") || r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]any{"authorize_url": authURL, "state": state})
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) handleGitHubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	cfg := s.githubOAuthConfig()
	app := appPublicURL()
	fail := func(msg string) {
		http.Redirect(w, r, app+"/app/team?github=error&message="+url.QueryEscape(msg), http.StatusFound)
	}
	if !cfg.Configured() {
		fail("oauth_not_configured")
		return
	}
	if errParam := strings.TrimSpace(r.URL.Query().Get("error")); errParam != "" {
		fail(errParam)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if code == "" || state == "" {
		fail("missing_code")
		return
	}
	ghOAuthMu.Lock()
	pending, ok := ghOAuthPending[state]
	delete(ghOAuthPending, state)
	ghOAuthMu.Unlock()
	if !ok || time.Now().After(pending.Expires) {
		fail("invalid_state")
		return
	}
	token, err := cfg.ExchangeCode(r.Context(), code)
	if err != nil {
		fail("exchange_failed")
		return
	}
	ghOAuthMu.Lock()
	ghOAuthTokens[pending.UserID] = oauthToken{Token: token, Expires: time.Now().Add(8 * time.Hour)}
	ghOAuthMu.Unlock()

	dest := app + "/app/team?github=linked&project=" + url.QueryEscape(pending.ProjectID)
	http.Redirect(w, r, dest, http.StatusFound)
}

func (s *Server) oauthTokenFor(userID string) string {
	ghOAuthMu.Lock()
	defer ghOAuthMu.Unlock()
	tok, ok := ghOAuthTokens[userID]
	if !ok || time.Now().After(tok.Expires) {
		return ""
	}
	return tok.Token
}

func (s *Server) handleGitHubRepos(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizePermission(w, r, id, store.PermMemberManage) {
		return
	}
	token := s.oauthTokenFor(authSubject(r))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if token == "" {
		writeError(w, http.StatusUnauthorized, "connect GitHub first (OAuth)")
		return
	}
	client := github.NewClient(token)
	repos, err := client.ListUserRepos(r.Context(), 50, 4)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if repos == nil {
		repos = []github.Repo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": repos, "count": len(repos)})
}

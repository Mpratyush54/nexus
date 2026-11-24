package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"central-memory/internal/config"
)

// authConnectTimeout bounds each probe so the setup wizard never hangs on a
// server that accepts the connection and then goes silent.
const authConnectTimeout = 10 * time.Second

// authConnect checks a server URL and, optionally, an API token. With
// save=true and a working token it becomes this device's sign-in. The
// desktop setup wizard uses it for "connect with a token"; browser sign-in
// stays on auth.login / auth.callback.
//
// Problems the user can fix (unreachable server, rejected token) come back
// as fields on a successful result, so the UI can show them inline. Only a
// malformed request is an error.
func authConnect(ctx context.Context, args json.RawMessage, client *http.Client) (any, error) {
	var request struct {
		ServerURL string `json:"server_url"`
		Token     string `json:"token"`
		Save      bool   `json:"save"`
	}
	if err := decode(args, &request); err != nil {
		return nil, err
	}
	server := strings.TrimRight(strings.TrimSpace(request.ServerURL), "/")
	parsed, err := url.Parse(server)
	if server == "" || err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, errors.New("enter a server URL that starts with https://")
	}
	if client == nil {
		client = &http.Client{Timeout: authConnectTimeout}
	}
	token := strings.TrimSpace(request.Token)
	out := map[string]any{
		"server_url":     server,
		"reachable":      false,
		"token_checked":  token != "",
		"token_valid":    false,
		"saved":          false,
		"env_server":     firstEnv("CENTRAL_SERVER_URL", "NEXUS_SERVER") != "",
		"env_token":      firstEnv("NEXUS_TOKEN", "CENTRAL_MEMORY_TOKEN", "CENTRAL_SERVER_TOKEN") != "",
		"server_version": "",
	}

	status, body, err := probe(ctx, client, server+"/healthz", "")
	if err != nil {
		out["message"] = "Could not reach " + parsed.Host + ". Check the address and your connection."
		out["detail"] = err.Error()
		return out, nil
	}
	if status != http.StatusOK {
		out["message"] = fmt.Sprintf("%s answered %d. Is this a Nexus server?", parsed.Host, status)
		return out, nil
	}
	var health struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(body, &health)
	out["reachable"] = true
	out["server_version"] = health.Version

	if token == "" {
		out["message"] = "Server is reachable."
		return out, nil
	}
	status, _, err = probe(ctx, client, server+"/projects", token)
	switch {
	case err != nil:
		out["message"] = "The server is up, but checking the token failed."
		out["detail"] = err.Error()
		return out, nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		out["message"] = "The server rejected this token. Copy a fresh one and try again."
		return out, nil
	case status != http.StatusOK:
		out["message"] = fmt.Sprintf("The server answered %d while checking the token.", status)
		return out, nil
	}
	out["token_valid"] = true
	out["message"] = "Connected."
	// Who the token belongs to (GET /users/me, the same call the web app
	// makes), so the app can say "Signed in as @name" and knows its own user
	// id for roles. A server without it still connects; the name stays empty.
	identity, identityErr := fetchIdentity(ctx, client, server, token)
	if identityErr == nil {
		out["user_id"] = identity.ID
		out["username"] = identity.Username
	}
	if !request.Save {
		return out, nil
	}
	// A different token may belong to a different person: never keep the
	// previous account's name next to it.
	if err := config.ClearCredentials(); err != nil {
		return nil, err
	}
	if err := config.SaveFile(config.File{ServerURL: server, Token: token, UserID: identity.ID, Username: identity.Username}); err != nil {
		return nil, err
	}
	out["saved"] = true
	out["status"] = authStatus()
	return out, nil
}

// accountIdentity is the signed-in person as GET /users/me reports it.
type accountIdentity struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func fetchIdentity(ctx context.Context, client *http.Client, server, token string) (accountIdentity, error) {
	var identity accountIdentity
	status, body, err := probe(ctx, client, strings.TrimRight(server, "/")+"/users/me", token)
	if err != nil {
		return identity, err
	}
	if status != http.StatusOK {
		return identity, fmt.Errorf("users/me answered %d", status)
	}
	if err := json.Unmarshal(body, &identity); err != nil {
		return identity, err
	}
	identity.ID = strings.TrimSpace(identity.ID)
	identity.Username = strings.TrimSpace(identity.Username)
	if identity.ID == "" {
		return identity, errors.New("users/me returned no id")
	}
	return identity, nil
}

// identityRefreshTimeout bounds the lazy identity lookup behind auth.status.
const identityRefreshTimeout = 4 * time.Second

// authStatusWithIdentity is auth.status for a sign-in saved without a name
// (token sign-in on an older build): it asks the server who the token
// belongs to once and keeps the answer, so the account shows a username
// and organization roles resolve. Any failure leaves the status as it was.
func authStatusWithIdentity(ctx context.Context) map[string]any {
	status := authStatus()
	if status["signed_in"] != true {
		return status
	}
	if id, _ := status["user_id"].(string); id != "" {
		if name, _ := status["username"].(string); name != "" {
			return status
		}
	}
	cfg, err := config.LoadFile()
	server := strings.TrimSpace(cfg.ServerURL)
	if v := firstEnv("CENTRAL_SERVER_URL", "NEXUS_SERVER"); v != "" {
		server = v
	}
	if err != nil || server == "" || strings.TrimSpace(cfg.Token) == "" {
		return status
	}
	ctx, cancel := context.WithTimeout(ctx, identityRefreshTimeout)
	defer cancel()
	identity, err := fetchIdentity(ctx, &http.Client{Timeout: identityRefreshTimeout}, server, strings.TrimSpace(cfg.Token))
	if err != nil {
		return status
	}
	if err := config.SaveFile(config.File{UserID: identity.ID, Username: identity.Username}); err != nil {
		return status
	}
	return authStatus()
}

// netProbeTimeout keeps the app's reachability check short: it runs on a
// schedule while the server is down and must not pile up.
const netProbeTimeout = 4 * time.Second

// netProbe asks the configured server's /healthz whether it answers at all.
// The OS network state (net.status) says nothing about a server that is
// down or unreachable; the desktop uses this to show and clear its offline
// banner. The raw error stays in "detail" for the log, never for people.
func netProbe(ctx context.Context) map[string]any {
	out := map[string]any{"reachable": false, "configured": false}
	cfg, _ := config.LoadFile()
	server := strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	if v := firstEnv("CENTRAL_SERVER_URL", "NEXUS_SERVER"); v != "" {
		server = strings.TrimRight(v, "/")
	}
	parsed, err := url.Parse(server)
	if server == "" || err != nil || parsed.Host == "" {
		return out
	}
	out["configured"] = true
	out["host"] = parsed.Host
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, netProbeTimeout)
	defer cancel()
	status, _, err := probe(ctx, &http.Client{Timeout: netProbeTimeout}, server+"/healthz", "")
	out["ms"] = time.Since(started).Milliseconds()
	if err != nil {
		out["detail"] = err.Error()
		return out
	}
	out["status"] = status
	// Any HTTP answer means the server is there; only 5xx from /healthz
	// (or a proxy in front of a stopped server) counts as down.
	out["reachable"] = status < http.StatusInternalServerError
	return out
}

func probe(ctx context.Context, client *http.Client, target, token string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, authConnectTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, body, nil
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

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
	if !request.Save {
		return out, nil
	}
	// A different token may belong to a different person: never keep the
	// previous account's name next to it.
	if err := config.ClearCredentials(); err != nil {
		return nil, err
	}
	if err := config.SaveFile(config.File{ServerURL: server, Token: token}); err != nil {
		return nil, err
	}
	out["saved"] = true
	out["status"] = authStatus()
	return out, nil
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

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Test-only browser bootstrap. The command's listener is loopback-only and its
// signing key, identities and data are ephemeral. Never mount this in production.
// All product pages and API requests still use the real frontend and handlers.
func demoWebHandler(api http.Handler, st demoState, root string) http.Handler {
	if root == "" {
		return api
	}
	assets := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "isolated demo requires a loopback host", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.StripPrefix("/api", api).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/demo/start" {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			token, identity := st.SenderToken, sender
			if r.URL.Query().Get("profile") == "recipient" {
				token, identity = st.RecipientToken, recipient
			}
			bootstrap, _ := json.Marshal(map[string]any{"token": token, "user": map[string]string{"userId": identity, "username": identity}, "projectId": st.ProjectID})
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><title>Isolated Nexus demo</title><script>const demo=%s;localStorage.setItem('nexus.auth.token',demo.token);localStorage.setItem('nexus.auth.user',JSON.stringify(demo.user));localStorage.setItem('nexus.project.id',demo.projectId);localStorage.setItem('nexus.onboarding.done','1');location.replace('/app/dashboard');</script>`, bootstrap)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/ws") {
			api.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			assets.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/app") || r.URL.Path == "/login" {
			// Refresh only this disposable browser profile after a demo-server restart.
			// Never installed in the production frontend or normal desktop profile.
			page, err := os.ReadFile(filepath.Join(root, "index.html"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			identities, _ := json.Marshal(map[string]any{
				"sender":    map[string]string{"token": st.SenderToken, "userId": sender, "username": sender},
				"recipient": map[string]string{"token": st.RecipientToken, "userId": recipient, "username": recipient},
				"project":   st.ProjectID,
			})
			boot := fmt.Sprintf(`<script>{const d=%s;let u={};try{u=JSON.parse(localStorage.getItem('nexus.auth.user')||'{}')}catch{}const a=u.userId===d.recipient.userId?d.recipient:d.sender;localStorage.setItem('nexus.auth.token',a.token);localStorage.setItem('nexus.auth.user',JSON.stringify({userId:a.userId,username:a.username}));localStorage.setItem('nexus.project.id',d.project);localStorage.setItem('nexus.onboarding.done','1');}</script>`, identities)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(strings.Replace(string(page), "<head>", "<head>"+boot, 1)))
			return
		}
		// Serve only files inside the explicit frontend build, never demo state.
		clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if clean == "." || strings.HasPrefix(clean, "..") || strings.Contains(clean, ":") {
			http.NotFound(w, r)
			return
		}
		if info, err := os.Stat(filepath.Join(root, clean)); err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		assets.ServeHTTP(w, r)
	})
}

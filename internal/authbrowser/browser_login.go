// Package authbrowser opens the Nexus web login and receives the token
// via a localhost callback (desktop / CLI "Sign in with browser" flow).
package authbrowser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"central-memory/internal/config"
)

// Result is the credential payload returned by /cli/auth → /callback.
type Result struct {
	Token    string
	UserID   string
	Username string
}

// Options controls the browser login bridge.
type Options struct {
	AppURL    string // e.g. https://nexus.pratyushes.dev
	ServerURL string // saved alongside credentials
	Timeout   time.Duration
	OpenURL   func(string) error // override for tests
}

// Login starts a loopback listener, opens the web app login page, and waits
// for the callback with token + user_id.
func Login(ctx context.Context, opt Options) (Result, error) {
	appURL := strings.TrimRight(strings.TrimSpace(opt.AppURL), "/")
	if appURL == "" {
		appURL = config.ResolveAppURL()
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	open := opt.OpenURL
	if open == nil {
		open = OpenBrowser
	}

	state, err := randomState()
	if err != nil {
		return Result{}, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Result{}, fmt.Errorf("authbrowser: listen: %w", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	resultCh := make(chan Result, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("state"); got != "" && got != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			errCh <- fmt.Errorf("authbrowser: state mismatch")
			return
		}
		tok := strings.TrimSpace(q.Get("token"))
		uid := strings.TrimSpace(q.Get("user_id"))
		user := strings.TrimSpace(q.Get("username"))
		if tok == "" || uid == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			errCh <- fmt.Errorf("authbrowser: callback missing token or user_id")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body style="font-family:Segoe UI,sans-serif;background:#0f1419;color:#e8eef4;display:flex;min-height:100vh;align-items:center;justify-content:center">
<div style="text-align:center"><h1>Signed in</h1><p>You can close this window and return to Nexus Desktop.</p></div>
<script>setTimeout(()=>window.close(),800)</script></body></html>`))
		resultCh <- Result{Token: tok, UserID: uid, Username: user}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, callbackURL, http.StatusFound)
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	authURL := appURL + "/cli/auth?" + url.Values{
		"redirect": {callbackURL},
		"state":    {state},
	}.Encode()

	if err := open(authURL); err != nil {
		return Result{}, fmt.Errorf("authbrowser: open browser: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-ctx.Done():
		return Result{}, fmt.Errorf("authbrowser: timed out waiting for browser login")
	case err := <-errCh:
		return Result{}, err
	case res := <-resultCh:
		server := strings.TrimSpace(opt.ServerURL)
		if server == "" {
			server = config.ResolveServerURL("")
		}
		if err := config.SaveFile(config.File{
			ServerURL: server,
			AppURL:    appURL,
			Token:     res.Token,
			UserID:    res.UserID,
			Username:  res.Username,
		}); err != nil {
			return res, fmt.Errorf("authbrowser: save config: %w", err)
		}
		// Give the browser a moment to render the success page.
		time.Sleep(300 * time.Millisecond)
		return res, nil
	}
}

// OpenBrowser opens url with the OS default handler.
func OpenBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	case "darwin":
		cmd = exec.Command("open", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}

func randomState() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

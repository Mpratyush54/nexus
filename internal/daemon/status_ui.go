package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"central-memory/internal/authbrowser"
	"central-memory/internal/config"
)

// ConnectionStatus is the JSON/HTML snapshot for the local status UI.
type ConnectionStatus struct {
	OK          bool   `json:"ok"`
	Connected   bool   `json:"connected"`
	ServerURL   string `json:"server_url"`
	AppURL      string `json:"app_url"`
	UserID      string `json:"user_id,omitempty"`
	Username    string `json:"username,omitempty"`
	HasToken    bool   `json:"has_token"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Root        string `json:"root,omitempty"`
	MachineID   string `json:"machine_id,omitempty"`
	ProxyURL    string `json:"proxy_url,omitempty"`
	Message     string `json:"message,omitempty"`
	StatusPage  string `json:"status_page,omitempty"`
}

// StatusSnapshot builds the current connection view for the status UI.
func (d *Daemon) StatusSnapshot() ConnectionStatus {
	cfg, _ := config.LoadFile()
	st := ConnectionStatus{
		OK:          true,
		ServerURL:   strings.TrimSpace(d.ServerURL),
		AppURL:      config.ResolveAppURL(),
		UserID:      firstNonEmpty(strings.TrimSpace(d.UserID), config.ResolveUserID(), strings.TrimSpace(cfg.UserID)),
		Username:    strings.TrimSpace(cfg.Username),
		HasToken:    strings.TrimSpace(d.ServerToken) != "" || strings.TrimSpace(cfg.Token) != "",
		WorkspaceID: d.getWorkspaceID(),
		Root:        d.Root,
		MachineID:   d.MachineID,
		ProxyURL:    d.GetProxyURL(),
		StatusPage:  "http://127.0.0.1:7272/",
	}
	if st.ServerURL == "" {
		st.ServerURL = config.ResolveServerURL("")
	}
	st.Connected = st.WorkspaceID != "" && st.HasToken
	switch {
	case !st.HasToken:
		st.Message = "Not signed in — log in below to connect this machine."
	case st.WorkspaceID == "":
		st.Message = "Signed in, but not registered with the server yet. Retrying…"
	default:
		st.Message = "Connected — this workspace is online."
	}
	return st
}

func (d *Daemon) handleLocalStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, d.StatusSnapshot())
}

func (d *Daemon) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	st := d.StatusSnapshot()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = statusPageTmpl.Execute(w, st)
}

type localLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (d *Daemon) handleLocalLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req localLoginRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read body")
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || strings.TrimSpace(req.Password) == "" {
		writeErr(w, http.StatusBadRequest, "username and password are required")
		return
	}
	server := strings.TrimSpace(d.ServerURL)
	if server == "" {
		server = config.ResolveServerURL("")
	}
	if server == "" {
		writeErr(w, http.StatusServiceUnavailable, "no server URL configured")
		return
	}
	token, userID, err := loginAgainstServer(r.Context(), server, username, req.Password)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := config.SaveFile(config.File{
		ServerURL: server,
		Token:     token,
		UserID:    userID,
		Username:  username,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save credentials: "+err.Error())
		return
	}
	d.ServerToken = token
	d.UserID = userID
	d.ServerURL = server

	regErr := d.Register(r.Context(), server)
	st := d.StatusSnapshot()
	if regErr != nil {
		st.Message = "Signed in, but register failed: " + regErr.Error()
		writeJSON(w, http.StatusOK, st)
		return
	}
	st.Connected = true
	st.Message = "Connected — this workspace is online."
	writeJSON(w, http.StatusOK, st)
}

func (d *Daemon) handleLocalBrowserLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Run browser login off the request goroutine; client polls /local/status.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		res, err := authbrowser.Login(ctx, authbrowser.Options{
			AppURL:    config.ResolveAppURL(),
			ServerURL: firstNonEmpty(strings.TrimSpace(d.ServerURL), config.ResolveServerURL("")),
		})
		if err != nil {
			log.Printf("daemon: browser login failed: %v", err)
			return
		}
		d.ServerToken = res.Token
		d.UserID = res.UserID
		if d.ServerURL == "" {
			d.ServerURL = config.ResolveServerURL("")
		}
		if err := d.Register(context.Background(), d.ServerURL); err != nil {
			log.Printf("daemon: register after browser login: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":      true,
		"message": "Opening Nexus login in your browser…",
	})
}

func (d *Daemon) handleLocalLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_ = config.ClearCredentials()
	d.ServerToken = ""
	d.UserID = ""
	writeJSON(w, http.StatusOK, d.StatusSnapshot())
}

func loginAgainstServer(ctx context.Context, server, username, password string) (token, userID string, err error) {
	payload, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(server, "/")+"/auth/login", bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("could not reach server: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var eresp struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &eresp)
		if eresp.Error != "" {
			return "", "", fmt.Errorf("%s", eresp.Error)
		}
		return "", "", fmt.Errorf("login failed (%s)", resp.Status)
	}
	var out struct {
		Token    string `json:"token"`
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", fmt.Errorf("bad login response")
	}
	if strings.TrimSpace(out.Token) == "" || strings.TrimSpace(out.UserID) == "" {
		return "", "", fmt.Errorf("login response missing token or user_id")
	}
	return strings.TrimSpace(out.Token), strings.TrimSpace(out.UserID), nil
}

var statusPageTmpl = template.Must(template.New("status").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Nexus Desktop</title>
<style>
  :root { --bg:#0f1419; --panel:#1a222c; --fg:#e8eef4; --dim:#8b9aab; --ok:#3dba74; --warn:#e0a23a; --bad:#e05858; --accent:#e8a54b; --border:#2a3542; }
  * { box-sizing: border-box; }
  body { margin:0; min-height:100vh; font:15px/1.45 "Segoe UI", system-ui, sans-serif; color:var(--fg);
    background: radial-gradient(1200px 600px at 10% -10%, #243044 0%, transparent 55%),
                radial-gradient(900px 500px at 100% 0%, #2a1f14 0%, transparent 50%), var(--bg); }
  main { max-width:440px; margin:0 auto; padding:48px 20px 64px; }
  h1 { font-size:1.55rem; font-weight:650; letter-spacing:-0.02em; margin:0 0 6px; }
  .sub { color:var(--dim); margin:0 0 28px; }
  .card { background:var(--panel); border:1px solid var(--border); border-radius:14px; padding:22px 22px 18px; }
  .row { display:flex; justify-content:space-between; gap:12px; padding:8px 0; border-bottom:1px solid var(--border); }
  .row:last-child { border-bottom:0; }
  .k { color:var(--dim); }
  .v { text-align:right; word-break:break-all; }
  .pill { display:inline-flex; align-items:center; gap:8px; font-weight:600; padding:6px 12px; border-radius:999px; font-size:13px; margin-bottom:16px; }
  .pill::before { content:""; width:8px; height:8px; border-radius:50%; background:currentColor; }
  .ok { color:var(--ok); background:rgba(61,186,116,.12); }
  .warn { color:var(--warn); background:rgba(224,162,58,.12); }
  .bad { color:var(--bad); background:rgba(224,88,88,.12); }
  .msg { margin:14px 0 0; color:var(--dim); font-size:13px; }
  form { margin-top:22px; display:grid; gap:12px; }
  label { display:grid; gap:6px; font-size:12px; color:var(--dim); }
  input { height:40px; border-radius:10px; border:1px solid var(--border); background:#121820; color:var(--fg); padding:0 12px; font:inherit; }
  input:focus { outline:none; border-color:var(--accent); }
  button { height:42px; border:0; border-radius:10px; background:var(--accent); color:#1a1208; font-weight:650; font:inherit; cursor:pointer; }
  button:disabled { opacity:.55; cursor:wait; }
  .ghost { background:transparent; color:var(--dim); border:1px solid var(--border); margin-top:10px; }
  a { color:var(--accent); }
  .err { color:var(--bad); font-size:13px; min-height:1.2em; }
  .foot { margin-top:18px; font-size:12px; color:var(--dim); }
</style>
</head>
<body>
<main>
  <h1>Nexus Desktop</h1>
  <p class="sub">Local workspace agent status</p>
  <div class="card">
    <div id="pill" class="pill {{if .Connected}}ok{{else if .HasToken}}warn{{else}}bad{{end}}">
      {{if .Connected}}Connected{{else if .HasToken}}Signed in — registering…{{else}}Not connected{{end}}
    </div>
    <div class="row"><span class="k">Server</span><span class="v">{{.ServerURL}}</span></div>
    <div class="row"><span class="k">User</span><span class="v">{{if .Username}}{{.Username}}{{else if .UserID}}{{.UserID}}{{else}}—{{end}}</span></div>
    <div class="row"><span class="k">Workspace</span><span class="v">{{if .WorkspaceID}}{{.WorkspaceID}}{{else}}—{{end}}</span></div>
    <div class="row"><span class="k">Folder</span><span class="v">{{.Root}}</span></div>
    <p class="msg" id="msg">{{.Message}}</p>

    <div id="loginBox" style="{{if .HasToken}}display:none{{end}}">
      <button type="button" id="browserLogin" style="width:100%;margin-top:18px">Sign in with Nexus…</button>
      <p class="err" id="err"></p>
      <p class="foot">Opens <a href="{{.AppURL}}/login" target="_blank" rel="noopener">{{.AppURL}}</a> in your browser, then returns here.</p>
    </div>
    <div id="logoutBox" style="{{if not .HasToken}}display:none{{end}}">
      <button type="button" class="ghost" id="logoutBtn" style="width:100%">Sign out</button>
      <p class="foot">Open the web app: <a href="{{.AppURL}}/app/dashboard" target="_blank" rel="noopener">{{.AppURL}}</a></p>
    </div>
  </div>
</main>
<script>
const pill = document.getElementById('pill');
const msg = document.getElementById('msg');
const err = document.getElementById('err');
const loginBox = document.getElementById('loginBox');
const logoutBox = document.getElementById('logoutBox');
const browserLogin = document.getElementById('browserLogin');
const logoutBtn = document.getElementById('logoutBtn');

function apply(st) {
  pill.className = 'pill ' + (st.connected ? 'ok' : (st.has_token ? 'warn' : 'bad'));
  pill.textContent = st.connected ? 'Connected' : (st.has_token ? 'Signed in — registering…' : 'Not connected');
  msg.textContent = st.message || '';
  loginBox.style.display = st.has_token ? 'none' : '';
  logoutBox.style.display = st.has_token ? '' : 'none';
}

browserLogin?.addEventListener('click', async () => {
  err.textContent = '';
  browserLogin.disabled = true;
  try {
    const res = await fetch('/local/browser-login', { method: 'POST' });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) { err.textContent = data.error || res.statusText; return; }
    msg.textContent = data.message || 'Complete sign-in in your browser…';
  } catch (x) {
    err.textContent = String(x);
  } finally {
    browserLogin.disabled = false;
  }
});

logoutBtn?.addEventListener('click', async () => {
  await fetch('/local/logout', { method: 'POST' });
  location.reload();
});

setInterval(async () => {
  try {
    const res = await fetch('/local/status');
    if (res.ok) apply(await res.json());
  } catch {}
}, 2000);
</script>
</body>
</html>
`))

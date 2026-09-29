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
	case strings.TrimSpace(st.Root) == "":
		st.Connected = false
		st.Message = "No workspace folder selected — please choose your project folder from the tray menu to start tracking AI agent memories."
	case !st.HasToken:
		st.Message = "Not signed in — log in below. Use the same Nexus account as the web portal."
	case st.WorkspaceID == "":
		st.Message = "Signed in, but not registered with the server yet. Retrying…"
	default:
		st.Message = "Connected — use the same account in the web portal or harvest stays locked."
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
	// Legacy debug HTML only. Product UI is the Fyne native shell
	// (cmd/nexus-desktop + internal/desktopui) — do not redesign this page.
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
<title>Nexus Cockpit</title>
<style>
  :root { --bg:#0f1419; --panel:rgba(26,34,44,.78); --fg:#e8eef4; --dim:#8b9aab; --ok:#3dba74; --warn:#e0a23a; --bad:#e05858; --accent:#e8a54b; --border:rgba(42,53,66,.85); --glass:backdrop-filter:blur(12px); }
  * { box-sizing: border-box; }
  body { margin:0; min-height:100vh; font:14px/1.45 "Segoe UI", system-ui, sans-serif; color:var(--fg);
    background: radial-gradient(1200px 600px at 10% -10%, #243044 0%, transparent 55%),
                radial-gradient(900px 500px at 100% 0%, #2a1f14 0%, transparent 50%), var(--bg); }
  main { max-width:1100px; margin:0 auto; padding:28px 20px 64px; display:grid; gap:16px; }
  @media (min-width:900px) { main { grid-template-columns: 1.1fr 1fr; } .span2 { grid-column:1 / -1; } }
  h1 { font-size:1.45rem; font-weight:650; letter-spacing:-0.02em; margin:0; }
  h2 { font-size:.95rem; font-weight:600; margin:0 0 10px; color:var(--fg); }
  .sub { color:var(--dim); margin:4px 0 0; font-size:13px; }
  .card { background:var(--panel); border:1px solid var(--border); border-radius:14px; padding:18px; backdrop-filter:blur(12px); }
  .row { display:flex; justify-content:space-between; gap:12px; padding:7px 0; border-bottom:1px solid var(--border); }
  .row:last-child { border-bottom:0; }
  .k { color:var(--dim); } .v { text-align:right; word-break:break-all; }
  .pill { display:inline-flex; align-items:center; gap:8px; font-weight:600; padding:6px 12px; border-radius:999px; font-size:12px; }
  .pill::before { content:""; width:8px; height:8px; border-radius:50%; background:currentColor; }
  .ok { color:var(--ok); background:rgba(61,186,116,.12); }
  .warn { color:var(--warn); background:rgba(224,162,58,.12); }
  .bad { color:var(--bad); background:rgba(224,88,88,.12); }
  .banner { margin:10px 0 0; padding:10px 12px; border-radius:10px; background:rgba(224,162,58,.12); border:1px solid rgba(224,162,58,.35); color:var(--warn); font-size:13px; }
  .msg { margin:10px 0 0; color:var(--dim); font-size:13px; }
  button { height:36px; border:0; border-radius:10px; background:var(--accent); color:#1a1208; font-weight:650; font:inherit; cursor:pointer; padding:0 14px; }
  button:disabled { opacity:.55; cursor:not-allowed; }
  .ghost { background:transparent; color:var(--dim); border:1px solid var(--border); }
  select { height:36px; border-radius:10px; border:1px solid var(--border); background:#121820; color:var(--fg); padding:0 10px; font:inherit; max-width:100%; }
  .hdr { display:flex; flex-wrap:wrap; align-items:flex-start; justify-content:space-between; gap:12px; }
  .ws { display:flex; flex-wrap:wrap; gap:8px; align-items:center; }
  .badge { display:inline-flex; gap:6px; align-items:center; padding:4px 10px; border-radius:999px; border:1px solid var(--border); color:var(--dim); font-size:12px; }
  #harness-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(140px,1fr)); gap:10px; }
  .hcard { border:1px solid var(--border); border-radius:12px; padding:12px; background:rgba(18,24,32,.55); }
  .hcard .name { font-weight:600; font-size:13px; }
  .hcard .meta { color:var(--dim); font-size:11px; margin-top:4px; }
  .dot { width:8px; height:8px; border-radius:50%; display:inline-block; background:var(--dim); }
  .dot.on { background:var(--ok); box-shadow:0 0 8px rgba(61,186,116,.5); }
  #activity-feed { max-height:280px; overflow:auto; font-family:ui-monospace,Consolas,monospace; font-size:12px; display:grid; gap:6px; }
  .ev { display:grid; grid-template-columns:72px 72px 1fr; gap:8px; padding:6px 8px; border-radius:8px; background:rgba(18,24,32,.45); }
  .ev .t { color:var(--dim); } .ev .kind { font-weight:600; }
  .kind-turn { color:#7ec8ff; } .kind-tool { color:#c4a5ff; } .kind-err { color:var(--bad); } .kind-ok { color:var(--ok); }
  .stats { display:grid; grid-template-columns:repeat(auto-fit,minmax(110px,1fr)); gap:10px; }
  .stat { border:1px solid var(--border); border-radius:12px; padding:10px; background:rgba(18,24,32,.4); }
  .stat .n { font-size:1.15rem; font-weight:650; } .stat .l { color:var(--dim); font-size:11px; margin-top:2px; }
  .actions { display:flex; flex-wrap:wrap; gap:8px; margin-top:12px; }
  details summary { cursor:pointer; color:var(--dim); font-weight:600; }
  .check { display:grid; gap:6px; margin-top:10px; font-size:13px; }
  .check div { display:flex; justify-content:space-between; gap:8px; }
  a { color:var(--accent); }
  .err { color:var(--bad); font-size:13px; min-height:1.2em; }
  .foot { margin-top:12px; font-size:12px; color:var(--dim); }
  .snap-row { display:flex; flex-wrap:wrap; justify-content:space-between; gap:8px; padding:8px 0; border-bottom:1px solid var(--border); font-size:13px; }
</style>
</head>
<body>
<main>
  <section class="card span2" id="header">
    <div class="hdr">
      <div>
        <h1>Nexus</h1>
        <p class="sub">Desktop Cockpit · <span id="folderName">{{.Root}}</span></p>
        <div style="margin-top:8px;display:flex;gap:8px;flex-wrap:wrap">
          <span class="badge" id="gitBadge">git —</span>
          <span class="badge" id="machineBadge">machine {{.MachineID}}</span>
        </div>
      </div>
      <div class="ws">
        <select id="wsSelect" title="Recent workspaces" aria-label="Recent workspaces"><option value="">Recent workspaces…</option></select>
        <button type="button" class="ghost" id="browseBtn" title="Use the tray menu → Switch workspace → Browse for folder… (browsers cannot open native folder pickers)" disabled>Browse Folder…</button>
      </div>
    </div>
    <div id="noWsBanner" class="banner" style="display:none">No workspace folder selected — please choose your project folder from the tray menu to start tracking AI agent memories.</div>
  </section>

  <section class="card" id="connection">
    <h2>Connection</h2>
    <div id="pill" class="pill {{if .Connected}}ok{{else if .HasToken}}warn{{else}}bad{{end}}">
      {{if .Connected}}Connected{{else if .HasToken}}Signed in — registering…{{else}}Not connected{{end}}
    </div>
    <div class="row"><span class="k">Server</span><span class="v" id="serverUrl">{{.ServerURL}}</span></div>
    <div class="row"><span class="k">User</span><span class="v" id="userLabel">{{if .Username}}{{.Username}}{{else if .UserID}}{{.UserID}}{{else}}—{{end}}</span></div>
    <div class="row"><span class="k">Workspace ID</span><span class="v" id="wsId">{{if .WorkspaceID}}{{.WorkspaceID}}{{else}}—{{end}}</span></div>
    <div class="row"><span class="k">Folder</span><span class="v" id="rootPath">{{.Root}}</span></div>
    <p class="msg" id="msg">{{.Message}}</p>
    <div id="loginBox" style="{{if .HasToken}}display:none{{end}}">
      <button type="button" id="browserLogin" style="width:100%;margin-top:14px">Sign in with Nexus…</button>
      <p class="err" id="err"></p>
      <p class="foot">Opens <a href="{{.AppURL}}/login" target="_blank" rel="noopener">{{.AppURL}}</a></p>
    </div>
    <div id="logoutBox" style="{{if not .HasToken}}display:none{{end}}">
      <button type="button" class="ghost" id="logoutBtn" style="width:100%;margin-top:14px">Sign out</button>
      <p class="foot"><a href="{{.AppURL}}/app/connect" target="_blank" rel="noopener">Desktop setup</a></p>
    </div>
  </section>

  <section class="card" id="stats-card">
    <h2>Stats</h2>
    <div class="stats" id="stats-row">
      <div class="stat"><div class="n" id="sLast">—</div><div class="l">Last scan</div></div>
      <div class="stat"><div class="n" id="sFiles">—</div><div class="l">Files · new turns</div></div>
      <div class="stat"><div class="n" id="sTurns">—</div><div class="l">Turns emitted</div></div>
      <div class="stat"><div class="n" id="sActive">—</div><div class="l">Active sessions</div></div>
      <div class="stat"><div class="n" id="sProps">—</div><div class="l">Proposals / errors</div></div>
    </div>
    <div class="actions">
      <button type="button" id="scanNow">Scan Now</button>
      <button type="button" class="ghost" id="refreshBtn">Refresh</button>
    </div>
  </section>

  <section class="card span2">
    <h2>Live Harnesses</h2>
    <div id="harness-grid"></div>
  </section>

  <section class="card span2">
    <h2>Activity</h2>
    <div id="activity-feed"></div>
  </section>

  <section class="card span2" id="teleport">
    <h2>Session Snapshots</h2>
    <p class="sub">Inbound snapshots from other machines (poll every 30s).</p>
    <div id="snapshot-list"></div>
  </section>

  <section class="card span2">
    <details id="diagPanel">
      <summary>Diagnostics</summary>
      <div class="check" id="diag-check"></div>
    </details>
  </section>
</main>
<script>
const pill = document.getElementById('pill');
const msg = document.getElementById('msg');
const err = document.getElementById('err');
const loginBox = document.getElementById('loginBox');
const logoutBox = document.getElementById('logoutBox');
const browserLogin = document.getElementById('browserLogin');
const logoutBtn = document.getElementById('logoutBtn');
const noWsBanner = document.getElementById('noWsBanner');
const machineId = {{printf "%q" .MachineID}};
const projectHint = {{printf "%q" .WorkspaceID}};

function baseName(p) {
  if (!p) return '—';
  const parts = String(p).replace(/\\/g,'/').split('/').filter(Boolean);
  return parts[parts.length-1] || p;
}
function applyStatus(st) {
  const noWs = !st.root;
  noWsBanner.style.display = noWs ? '' : 'none';
  let cls = 'bad', label = 'Not connected';
  if (noWs) { cls = 'warn'; label = 'No workspace selected'; }
  else if (st.connected) { cls = 'ok'; label = 'Connected'; }
  else if (st.has_token) { cls = 'warn'; label = 'Signed in — registering…'; }
  pill.className = 'pill ' + cls;
  pill.textContent = label;
  msg.textContent = st.message || '';
  loginBox.style.display = st.has_token ? 'none' : '';
  logoutBox.style.display = st.has_token ? '' : 'none';
  document.getElementById('folderName').textContent = baseName(st.root) || '—';
  document.getElementById('rootPath').textContent = st.root || '—';
  document.getElementById('wsId').textContent = st.workspace_id || '—';
  document.getElementById('serverUrl').textContent = st.server_url || '—';
  document.getElementById('userLabel').textContent = st.username || st.user_id || '—';
  document.getElementById('machineBadge').textContent = 'machine ' + (st.machine_id || '—');
}

async function loadRecent() {
  try {
    const res = await fetch('/local/workspace/recent');
    if (!res.ok) return;
    const data = await res.json();
    const items = Array.isArray(data) ? data : (data.items || []);
    const sel = document.getElementById('wsSelect');
    const cur = sel.value;
    sel.innerHTML = '<option value="">Recent workspaces…</option>';
    items.forEach(p => {
      const o = document.createElement('option');
      o.value = p; o.textContent = baseName(p) + ' — ' + p;
      sel.appendChild(o);
    });
    if (cur) sel.value = cur;
  } catch {}
}

document.getElementById('wsSelect').addEventListener('change', async (e) => {
  const path = e.target.value;
  if (!path) return;
  const res = await fetch('/local/workspace/switch', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({path}) });
  const data = await res.json().catch(()=>({}));
  msg.textContent = data.message || (res.ok ? 'Workspace saved — restart daemon from tray.' : (data.error || 'switch failed'));
});

browserLogin?.addEventListener('click', async () => {
  err.textContent = '';
  browserLogin.disabled = true;
  try {
    const res = await fetch('/local/browser-login', { method: 'POST' });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) { err.textContent = data.error || res.statusText; return; }
    msg.textContent = data.message || 'Complete sign-in in your browser…';
  } catch (x) { err.textContent = String(x); }
  finally { browserLogin.disabled = false; }
});
logoutBtn?.addEventListener('click', async () => { await fetch('/local/logout', { method: 'POST' }); location.reload(); });

function renderHarvest(h) {
  const grid = document.getElementById('harness-grid');
  grid.innerHTML = '';
  (h.agents || []).forEach(a => {
    const el = document.createElement('div');
    el.className = 'hcard';
    const active = (a.files_seen || a.file_count || 0) > 0;
    el.innerHTML = '<div class="name"><span class="dot '+(active?'on':'')+'"></span> '+(a.name||a.agent||'agent')+'</div>'
      + '<div class="meta">'+(a.format||a.kind||'—')+' · files '+(a.files_seen ?? a.file_count ?? 0)+'</div>';
    grid.appendChild(el);
  });
  const feed = document.getElementById('activity-feed');
  feed.innerHTML = '';
  (h.recent || []).forEach(ev => {
    const row = document.createElement('div');
    row.className = 'ev';
    const kind = (ev.type || ev.event || 'turn').toLowerCase();
    let kc = 'kind-turn';
    if (kind.includes('tool')) kc = 'kind-tool';
    if (kind.includes('err')) kc = 'kind-err';
    if (kind.includes('ok') || kind.includes('saved')) kc = 'kind-ok';
    row.innerHTML = '<span class="t">'+(ev.at || ev.time || '').toString().slice(11,19)+'</span>'
      + '<span class="kind '+kc+'">'+(ev.type||ev.event||'event')+'</span>'
      + '<span>'+(ev.agent||'')+' '+(ev.detail||ev.message||'')+'</span>';
    feed.appendChild(row);
  });
  feed.scrollTop = feed.scrollHeight;
  document.getElementById('sLast').textContent = h.last_scan_at ? new Date(h.last_scan_at).toLocaleTimeString() : '—';
  document.getElementById('sFiles').textContent = (h.last_scan_files ?? '—') + ' · ' + (h.last_scan_turns ?? '—');
  document.getElementById('sTurns').textContent = h.turns_emitted ?? '—';
  document.getElementById('sActive').textContent = h.active_sessions ?? '—';
  document.getElementById('sProps').textContent = (h.proposals_saved ?? 0) + ' / ' + (h.proposal_errors ?? 0);
  if (h.root) document.getElementById('gitBadge').textContent = 'folder ' + baseName(h.root);
}

async function refreshHarvest() {
  try {
    const res = await fetch('/local/harvest');
    if (res.ok) renderHarvest(await res.json());
  } catch {}
}
document.getElementById('scanNow').addEventListener('click', async () => {
  await fetch('/local/harvest', { method:'POST' });
  await refreshHarvest();
});
document.getElementById('refreshBtn').addEventListener('click', async () => {
  const res = await fetch('/local/status');
  if (res.ok) applyStatus(await res.json());
  await refreshHarvest();
  await loadRecent();
});

document.getElementById('diagPanel').addEventListener('toggle', async (e) => {
  if (!e.target.open) return;
  const box = document.getElementById('diag-check');
  box.textContent = 'Running…';
  try {
    const res = await fetch('/local/diagnostics');
    const d = await res.json();
    const lines = [
      ['Server connectivity', d.server_reachable],
      ['Token valid', d.token_valid],
      ['Workspace linked', d.workspace_linked],
      ['Git available', d.git_available],
    ];
    Object.entries(d.harness_paths_found || {}).forEach(([k,v]) => lines.push(['Harness '+k, v]));
    box.innerHTML = lines.map(([k,v]) => '<div><span>'+k+'</span><span>'+(v?'✅':'❌')+'</span></div>').join('');
  } catch (x) { box.textContent = String(x); }
});

async function loadSnapshots() {
  const list = document.getElementById('snapshot-list');
  try {
    const st = await (await fetch('/local/status')).json();
    const pid = st.workspace_id || projectHint;
    if (!pid || !st.server_url || !st.has_token) {
      list.innerHTML = '<p class="sub">Sign in and link a workspace to see teleport snapshots.</p>';
      return;
    }
    // Snapshots are listed via local proxy helper when available; otherwise show CLI hint.
    const res = await fetch('/local/snapshots').catch(()=>null);
    if (!res || !res.ok) {
      list.innerHTML = '<p class="sub">Use <code>nexus session restore &lt;id&gt; --workspace &lt;path&gt;</code> or open portal Sessions.</p>';
      return;
    }
    const data = await res.json();
    const items = (data.items || []).filter(s => s.source_machine_id && s.source_machine_id !== (st.machine_id || machineId));
    if (!items.length) { list.innerHTML = '<p class="sub">No inbound snapshots from other machines.</p>'; return; }
    list.innerHTML = '';
    items.forEach(s => {
      const row = document.createElement('div');
      row.className = 'snap-row';
      row.innerHTML = '<div><strong>'+(s.harness||'')+'</strong> · '+(s.turn_count||0)+' turns · '+(s.git_branch||'')+' · from '+(s.source_machine_id||'')+'</div>';
      const actions = document.createElement('div');
      actions.style.display = 'flex'; actions.style.gap = '6px';
      const restore = document.createElement('button');
      restore.textContent = 'Restore Here';
      restore.onclick = async () => {
        const body = { session_id: s.session_id, workspace: st.root };
        const r = await fetch('/local/session/restore', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body) });
        const out = await r.json().catch(()=>({}));
        msg.textContent = out.message || out.error || (r.ok ? 'Restored' : 'Restore failed');
      };
      const copy = document.createElement('button');
      copy.className = 'ghost';
      copy.textContent = 'Copy CLI';
      copy.onclick = () => navigator.clipboard.writeText('nexus session restore '+s.session_id+' --workspace "'+(st.root||'')+'"');
      actions.append(restore, copy);
      row.appendChild(actions);
      list.appendChild(row);
    });
  } catch (x) {
    list.innerHTML = '<p class="sub">'+String(x)+'</p>';
  }
}

setInterval(async () => {
  try {
    const res = await fetch('/local/status');
    if (res.ok) applyStatus(await res.json());
  } catch {}
}, 2000);
setInterval(refreshHarvest, 4000);
setInterval(loadSnapshots, 30000);
loadRecent();
refreshHarvest();
loadSnapshots();
</script>
</body>
</html>
`))

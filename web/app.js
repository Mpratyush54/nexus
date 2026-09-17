"use strict";
/* Central Memory dashboard (issue #14) — static SPA, no build step.
 *
 * Server contracts (must match; see docs/issues/ISSUE-14.md checklist):
 *
 * REST (implemented, internal/server/routes.go):
 *   POST /auth/login  {username,password} -> {token,expires_at,user_id}
 *   GET  /memory/search?project_id=&q=&tags=&key=&level=&limit= -> {items,count}
 *     item: {id,project_id,key,content,context_snippet,level,scope,tags,
 *            confidence,status,source,created_at}
 *     NOTE: no `status` query param — status filters client-side.
 *   POST /memory {project_id,key,content,...} -> 201 item
 *   GET  /episodes/search?project_id=&q=&error_pattern=&file=&status=
 *        &episode_type=&limit= -> {episodes,count}
 *     episode: {id,project_id,title,episode_type,trigger,investigation,
 *       root_cause,resolution,verification,tags,files_involved,
 *       error_patterns,status,opened_at}
 *   GET  /workspaces/{projectID}/active -> {workspaces,count}
 *   GET  /healthz -> {status:"ok"}
 *   Errors: {"error":"..."} envelope.
 *
 * Lifecycle (NOT yet implemented server-side — follow-up, see ADR-014):
 *   POST /memory/{id}/confirm | POST /memory/{id}/reject
 *   POST /memory/{id}/promote {level}
 *   Buttons call these; a 404 surfaces the pending-endpoint notice.
 *
 * WebSocket (plan §3.2 shapes; endpoint path is a PROPOSAL for issue #13,
 * which owns internal/server/ws.go — no ws.go exists yet):
 *   WS_PATH = "/ws"
 *   Client -> Server:
 *     {"type":"subscribe","project_id":"...","session_id":"..."}
 *     {"type":"action","event_type":"MESSAGE_SENT","payload":{...}}
 *     {"type":"presence","status":"typing"}
 *   Server -> Client:
 *     {"type":"event","event":{...full event row...}}
 *     {"type":"presence","user_id":"...","status":"online|typing|idle|offline"}
 *     {"type":"memory_update","item":{...},"action":"proposed|confirmed|rejected"}
 *     {"type":"episode_update","episode":{...},"action":"opened|updated|resolved"}
 */

const WS_PATH = "/ws"; // proposed path for #13's hub; see note above
const FEED_CAP = 200;

const state = {
  token: sessionStorage.getItem("cm.token") || "",
  userID: sessionStorage.getItem("cm.user_id") || "",
  ws: null,
  wsWanted: false,
  retryMs: 1000,
  presence: new Map(), // user_id -> status
  episodes: [], // last search result, for the detail pane
};

const $ = (id) => document.getElementById(id);

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function now() {
  return new Date().toISOString().slice(11, 19);
}

/* ---------------- REST ---------------- */

async function api(path, { method = "GET", body } = {}) {
  const headers = {};
  if (state.token) headers.Authorization = "Bearer " + state.token;
  let payload;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  const res = await fetch(path, { method, headers, body: payload });
  const text = await res.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    throw new Error(`HTTP ${res.status}: non-JSON response`);
  }
  if (!res.ok) {
    throw new Error(`HTTP ${res.status}: ${(data && data.error) || text || "request failed"}`);
  }
  return data;
}

function requireProject() {
  const id = $("project-id").value.trim();
  if (!id) throw new Error("Enter a Project ID first.");
  return id;
}

function note(msg, isErr) {
  const el = $("toolbar-msg");
  el.textContent = msg;
  el.style.color = isErr ? "var(--bad)" : "";
}

/* ---------------- auth ---------------- */

async function login(ev) {
  ev.preventDefault();
  const username = $("login-user").value.trim();
  const password = $("login-pass").value;
  if (!username || !password) {
    $("login-state").textContent = "username + password required";
    return;
  }
  try {
    // POST /auth/login {username,password} -> {token,expires_at,user_id}
    const data = await api("/auth/login", { method: "POST", body: { username, password } });
    state.token = data.token;
    state.userID = data.user_id;
    sessionStorage.setItem("cm.token", state.token);
    sessionStorage.setItem("cm.user_id", state.userID);
    $("login-state").textContent = `ok (${data.user_id})`;
  } catch (err) {
    $("login-state").textContent = String(err.message || err);
  }
}

function refreshLoginState() {
  if (state.token) $("login-state").textContent = `token set (${state.userID || "?"})`;
}

/* ---------------- WebSocket live feed ---------------- */

function wsURL() {
  const scheme = location.protocol === "https:" ? "wss://" : "ws://";
  // Token via query: browsers cannot set Authorization headers on upgrade.
  // #13 decides the auth scheme; the hub should also accept header-based
  // auth from non-browser clients. Works without a token too (server 401s).
  const q = state.token ? `?token=${encodeURIComponent(state.token)}` : "";
  return scheme + location.host + WS_PATH + q;
}

function setConn(which, label) {
  const dot = $("conn-dot");
  dot.className = `dot ${which}`;
  $("conn-label").textContent = label;
}

function connectLive() {
  state.wsWanted = true;
  state.retryMs = 1000;
  openWS();
}

function openWS() {
  if (state.ws) state.ws.close();
  setConn("connecting", "connecting…");
  let ws;
  try {
    ws = new WebSocket(wsURL());
  } catch (err) {
    setConn("offline", "bad WS URL");
    scheduleRetry();
    return;
  }
  state.ws = ws;

  ws.onopen = () => {
    setConn("online", "live");
    state.retryMs = 1000;
    // Client -> Server subscribe (plan §3.2 verbatim shape).
    const msg = { type: "subscribe" };
    try {
      msg.project_id = requireProject();
    } catch {
      msg.project_id = $("project-id").value.trim(); // may be empty; hub validates
    }
    const sess = $("session-id").value.trim();
    if (sess) msg.session_id = sess;
    ws.send(JSON.stringify(msg));
    note("subscribed: " + JSON.stringify(msg));
  };

  ws.onmessage = (ev) => {
    let msg;
    try {
      msg = JSON.parse(ev.data);
    } catch {
      return; // ignore non-JSON frames
    }
    handleWSMessage(msg);
  };

  ws.onclose = () => {
    if (state.ws === ws) state.ws = null;
    setConn("offline", "disconnected");
    if (state.wsWanted) scheduleRetry();
  };

  ws.onerror = () => {
    // onclose follows; keep handling there.
  };
}

function scheduleRetry() {
  const ms = Math.min(state.retryMs, 15000);
  setConn("offline", `retrying in ${Math.round(ms / 1000)}s…`);
  setTimeout(() => {
    if (state.wsWanted && !state.ws) {
      state.retryMs *= 2;
      openWS();
    }
  }, ms);
}

function sendPresence(status) {
  // Client -> Server presence (plan §3.2): {"type":"presence","status":"typing"}
  if (state.ws && state.ws.readyState === WebSocket.OPEN) {
    state.ws.send(JSON.stringify({ type: "presence", status }));
  }
}

function handleWSMessage(msg) {
  // Server -> Client shapes (plan §3.2): event | presence |
  // memory_update | episode_update. Unknown types render generically.
  switch (msg.type) {
    case "event":
      pushFeed("ev-event", describeEvent(msg.event));
      break;
    case "presence":
      if (msg.user_id) {
        state.presence.set(msg.user_id, msg.status || "?");
        renderPresence();
      }
      pushFeed("ev-presence", `<b>${esc(msg.user_id)}</b> is ${esc(msg.status)}`);
      break;
    case "memory_update":
      pushFeed("ev-memory_update",
        `memory <b>${esc(msg.action)}</b>: ${esc(msg.item && msg.item.key)}`);
      refreshMemoriesSilent(); // targeted re-fetch, no full refresh
      break;
    case "episode_update":
      pushFeed("ev-episode_update",
        `episode <b>${esc(msg.action)}</b>: ${esc(msg.episode && msg.episode.title)}`);
      break;
    default:
      pushFeed("", esc(JSON.stringify(msg)));
  }
}

function describeEvent(ev) {
  if (!ev || typeof ev !== "object") return esc(JSON.stringify(ev));
  const kind = ev.event_type || "event";
  const who = ev.user_id || ev.agent_id || "system";
  return `<b>${esc(kind)}</b> <span class="meta">${esc(who)}${ev.project_id ? " · " + esc(ev.project_id) : ""}</span>`;
}

function pushFeed(cls, html) {
  const ul = $("feed");
  const li = document.createElement("li");
  if (cls) li.className = cls;
  li.innerHTML = `<time>${esc(now())}</time><span>${html}</span>`;
  ul.prepend(li);
  while (ul.children.length > FEED_CAP) ul.lastChild.remove();
  $("feed-count").textContent = String(ul.children.length);
}

/* ---------------- sessions & participants ---------------- */

function renderPresence() {
  const ul = $("presence");
  ul.innerHTML = "";
  if (state.presence.size === 0) {
    ul.innerHTML = `<li class="muted">No presence seen yet — connect live first.</li>`;
    return;
  }
  for (const [user, status] of state.presence) {
    const li = document.createElement("li");
    li.innerHTML = `<b>${esc(user)}</b> <span class="meta">${esc(status)}</span>`;
    ul.appendChild(li);
  }
}

async function refreshWorkspaces() {
  // GET /workspaces/{projectID}/active -> {workspaces,count}
  const ul = $("workspaces");
  let projectID;
  try {
    projectID = requireProject();
  } catch (err) {
    ul.innerHTML = `<li class="muted">${esc(err.message)}</li>`;
    return;
  }
  try {
    const data = await api(`/workspaces/${encodeURIComponent(projectID)}/active`);
    ul.innerHTML = "";
    if (!data.workspaces || data.workspaces.length === 0) {
      ul.innerHTML = `<li class="muted">No active workspaces.</li>`;
      return;
    }
    for (const w of data.workspaces) {
      const li = document.createElement("li");
      li.innerHTML =
        `<b>${esc(w.machine_id || w.id)}</b> ` +
        `<span class="meta">${esc(w.user_id || "")} · ${esc(w.branch || "")}@${esc((w.commit_sha || "").slice(0, 8))}${w.is_dirty ? " · dirty" : ""}</span><br />` +
        `<span class="meta">${esc(w.path || "")}</span>`;
      ul.appendChild(li);
    }
  } catch (err) {
    ul.innerHTML = `<li class="muted">${esc(err.message || err)}</li>`;
  }
}

/* ---------------- Memory Explorer ---------------- */

async function searchMemories(silent) {
  // GET /memory/search?project_id=&q=&tags=&key=&level=&limit=
  const ul = $("memories");
  let projectID;
  try {
    projectID = requireProject();
  } catch (err) {
    if (!silent) ul.innerHTML = `<li class="muted">${esc(err.message)}</li>`;
    return;
  }
  const params = new URLSearchParams({ project_id: projectID });
  const q = $("mem-q").value.trim();
  const level = $("mem-level").value; // wire values are lowercase already
  const status = $("mem-status").value; // NO server param — filter client-side
  if (q) params.set("q", q);
  if (level) params.set("level", level);
  params.set("limit", "50");
  try {
    const data = await api(`/memory/search?${params.toString()}`);
    let items = data.items || [];
    if (status) items = items.filter((m) => m.status === status);
    ul.innerHTML = "";
    if (items.length === 0) {
      ul.innerHTML = `<li class="muted">No memories match.</li>`;
      return;
    }
    for (const m of items) ul.appendChild(memoryRow(m));
  } catch (err) {
    if (!silent) ul.innerHTML = `<li class="muted">${esc(err.message || err)}</li>`;
  }
}

function refreshMemoriesSilent() {
  // Live WS update path: re-run the current filter, never location.reload().
  if ($("project-id").value.trim()) searchMemories(true).catch(() => {});
}

function memoryRow(m) {
  const li = document.createElement("li");
  li.dataset.id = m.id;
  const tags = (m.tags || []).map((t) => `<span class="tag">${esc(t)}</span>`).join("");
  li.innerHTML =
    `<b>${esc(m.key)}</b> ` +
    `<span class="tag">${esc(m.level)}</span><span class="tag">${esc(m.status)}</span>` +
    `<span class="meta">conf ${esc(m.confidence)} · ${esc(m.scope || "")}</span>` +
    `<p>${esc(m.content)}</p>` +
    (m.context_snippet ? `<p class="meta">${esc(m.context_snippet)}</p>` : "") +
    (tags ? `<p>${tags}</p>` : "") +
    `<div class="btn-row">
       <button type="button" data-act="confirm">Confirm</button>
       <button type="button" data-act="reject">Reject</button>
       <button type="button" data-act="promote">Promote</button>
     </div>`;
  li.querySelectorAll("button").forEach((b) =>
    b.addEventListener("click", () => memoryAction(m.id, b.dataset.act, b)));
  return li;
}

function nextLevel(level) {
  // session -> personal -> project -> organization
  const order = ["session", "personal", "project", "organization"];
  const i = order.indexOf(level);
  return i < 0 || i === order.length - 1 ? "project" : order[i + 1];
}

async function memoryAction(id, act, btn) {
  // Lifecycle endpoints are PROPOSED (server follow-up, see ADR-014):
  //   POST /memory/{id}/confirm | /reject | /promote {level}
  btn.disabled = true;
  try {
    const body = act === "promote"
      ? { level: nextLevel(document.querySelector(`li[data-id="${CSS.escape(id)}"] .tag`)?.textContent || "session") }
      : {};
    await api(`/memory/${encodeURIComponent(id)}/${act}`, { method: "POST", body });
    note(`memory ${act} ok (${id})`);
    searchMemories(true).catch(() => {});
  } catch (err) {
    note(`memory ${act} failed: ${err.message} — lifecycle endpoints are a server follow-up (see footer).`, true);
  } finally {
    btn.disabled = false;
  }
}

/* ---------------- Episode Inspector ---------------- */

async function searchEpisodes() {
  // GET /episodes/search?project_id=&q=&error_pattern=&file=&status=&episode_type=&limit=
  const ul = $("episodes");
  let projectID;
  try {
    projectID = requireProject();
  } catch (err) {
    ul.innerHTML = `<li class="muted">${esc(err.message)}</li>`;
    return;
  }
  const params = new URLSearchParams({ project_id: projectID });
  const q = $("ep-q").value.trim();
  const errPat = $("ep-error").value.trim();
  const file = $("ep-file").value.trim();
  const status = $("ep-status").value;
  if (q) params.set("q", q);
  if (errPat) params.set("error_pattern", errPat);
  if (file) params.set("file", file);
  if (status) params.set("status", status);
  params.set("limit", "50");
  try {
    const data = await api(`/episodes/search?${params.toString()}`);
    state.episodes = data.episodes || [];
    ul.innerHTML = "";
    if (state.episodes.length === 0) {
      ul.innerHTML = `<li class="muted">No episodes match.</li>`;
      return;
    }
    state.episodes.forEach((ep, i) => {
      const li = document.createElement("li");
      li.innerHTML =
        `<b>${esc(ep.title)}</b><br />` +
        `<span class="tag">${esc(ep.episode_type)}</span>` +
        `<span class="tag">${esc(ep.status)}</span>` +
        `<div class="btn-row"><button type="button">Inspect</button></div>`;
      li.querySelector("button").addEventListener("click", () => showEpisode(i));
      ul.appendChild(li);
    });
  } catch (err) {
    ul.innerHTML = `<li class="muted">${esc(err.message || err)}</li>`;
  }
}

function showEpisode(i) {
  // trigger → root-cause → fix view from the episode arc fields.
  const ep = state.episodes[i];
  const detail = $("episode-detail");
  if (!ep) return;
  const section = (h, v) => (v ? `<h3>${h}</h3><p>${esc(v)}</p>` : "");
  detail.innerHTML =
    `<h2>${esc(ep.title)}</h2>` +
    `<p class="meta">${esc(ep.episode_type)} · ${esc(ep.status)} · opened ${esc(ep.opened_at || "")}</p>` +
    ((ep.error_patterns || []).map((t) => `<span class="tag">err: ${esc(t)}</span>`).join("") +
      (ep.files_involved || []).map((t) => `<span class="tag">file: ${esc(t)}</span>`).join("") +
      (ep.tags || []).map((t) => `<span class="tag">${esc(t)}</span>`).join("")) +
    section("Trigger", ep.trigger) +
    section("Investigation", ep.investigation) +
    section("Root cause", ep.root_cause || ep.rootCause) +
    section("Fix", ep.resolution) +
    section("Verification", ep.verification);
}

/* ---------------- misc ---------------- */

async function checkHealth() {
  try {
    const data = await api("/healthz");
    note(`health: ${JSON.stringify(data)}`);
  } catch (err) {
    note(`health failed: ${err.message}`, true);
  }
}

/* ---------------- wiring ---------------- */

$("login-form").addEventListener("submit", login);
$("connect-btn").addEventListener("click", connectLive);
$("health-btn").addEventListener("click", checkHealth);
$("refresh-workspaces").addEventListener("click", refreshWorkspaces);
$("memory-filter").addEventListener("submit", (e) => { e.preventDefault(); searchMemories(false); });
$("episode-filter").addEventListener("submit", (e) => { e.preventDefault(); searchEpisodes(); });
// Typing presence: light touch — signal on search typing (plan §3.2 presence).
$("mem-q").addEventListener("input", () => sendPresence("typing"));
window.addEventListener("beforeunload", () => { state.wsWanted = false; });

refreshLoginState();
renderPresence();

/* Central Memory — Multiplayer Live Feed dashboard (Phase 3, nexus issue #14).
 *
 * Static SPA, vanilla JS, zero dependencies. Speaks:
 *   REST: internal/server/routes.go + routes_extra.go
 *     POST /auth/login
 *     POST /projects/resolve
 *     GET  /workspaces/{projectId}/active
 *     GET  /memory/search?project_id&q&tags&limit      -> {items,count}
 *     GET  /episodes/search?project_id&q&error_pattern&limit -> {items,count}
 *     POST /memory/{id}/confirm | POST /memory/{id}/reject | POST /memory/{id}/promote
 *        (confirmation-flow endpoints live in routes_extra.go/promote.go;
 *         calls still fall back to optimistic local state on error.)
 *   WS: internal/server/ws.go (stdlib hub). Auth: ?token= query (browsers
 *     cannot set headers on WebSocket) or Authorization header; the client
 *     appends ?token= automatically when a token exists.
 *     C->S: {"type":"subscribe","project_id":"...","session_id":"..."}
 *           {"type":"action","event_type":"MESSAGE_SENT","payload":{...}}
 *           {"type":"presence","status":"typing"}
 *     S->C: {"type":"event","event":{...}}
 *           {"type":"presence","user_id":"...","status":"online|typing|idle|offline"}
 *           {"type":"memory_update","item":{...},"action":"proposed|confirmed|rejected"}
 *           {"type":"episode_update","episode":{...},"action":"opened|updated|resolved"}
 *           {"type":"subscribed","project_id":"...","session_id":"..."} (ack)
 *           {"type":"error","message":"..."} (e.g. bad subscribe, expired token)
 *
 * Deploy defaults (issue #130): same-origin API/WS derived from
 * window.location (https -> wss), editable overrides persisted to
 * localStorage. Behind ALB/HTTPS, ws:// would be mixed-content-blocked, so
 * the default is never hardcoded ws://.
 *
 * If the WS endpoint is absent (404 / refused), the dashboard stays usable over
 * REST and retries the socket with backoff.
 */
'use strict';

const $ = (id) => document.getElementById(id);
const els = {};
['apiBase','wsUrl','username','password','authState','wsDot','wsLabel','latency',
 'canonicalUrl','rootCommit','folderName','projectId','sessionId','agentName',
 'projectState','presenceList','presenceCount','feed','feedCount','feedFilter',
 'memoryList','memLevel','memStatus','episodeList','searchBox',
 'memSearchResults','memSearchCount','epSearchResults','epSearchCount'
].forEach((id) => { els[id] = $(id); });

const store = {
  load(k, d) { try { const v = localStorage.getItem('cm.' + k); return v == null ? d : v; } catch { return d; } },
  save(k, v) { try { localStorage.setItem('cm.' + k, v); } catch {} },
};

const state = {
  token: store.load('token', ''),
  projectId: store.load('projectId', ''),
  sessionId: store.load('sessionId', ''),
  ws: null,
  wsWanted: false,
  retryMs: 1000,
  presence: new Map(), // user_id -> {status, at}
  feedTotal: 0,
  pollTimer: null,
};

// ---------- small utils ----------
function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function apiBase() { return els.apiBase.value.trim().replace(/\/+$/, ''); }
// Same-origin defaults (issue #130): http->http/ws, https->https/wss, WS on
// path /ws. Manual overrides in the inputs (persisted) always win.
function defaultApiBase() {
  try {
    return window.location.origin && window.location.origin !== 'null'
      ? window.location.origin
      : 'http://localhost:8080';
  } catch { return 'http://localhost:8080'; }
}
function defaultWsUrl() {
  try {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const host = window.location.host || 'localhost:8080';
    if (!window.location.host) return 'ws://localhost:8080/ws';
    return proto + '//' + host + '/ws';
  } catch { return 'ws://localhost:8080/ws'; }
}
function authHeaders() {
  const h = { 'Content-Type': 'application/json' };
  if (state.token) h.Authorization = 'Bearer ' + state.token;
  return h;
}
function fmtTime(iso) {
  try { return new Date(iso).toLocaleTimeString(); } catch { return ''; }
}
async function rest(path, opts) {
  const res = await fetch(apiBase() + path, opts);
  const text = await res.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch { body = { _raw: text }; }
  if (!res.ok) {
    if (res.status === 401) onUnauthorized();
    const msg = (body && body.error && body.error.message) || ('HTTP ' + res.status);
    throw new Error(msg);
  }
  return body;
}

// 401 -> re-login flow (issue #130): expired tokens surfaced as bare HTTP
// 401 before. Drop the dead token and tell the user to log in again.
// NOTE: the token lives in localStorage (XSS-readable by design for a
// static SPA with no httpOnly-cookie backend); treat the dashboard host as
// trusted and prefer short TTLs server-side.
function onUnauthorized() {
  state.token = '';
  store.save('token', '');
  els.authState.textContent = 'session expired (401) — login again';
  disconnectWs();
}

// ---------- connection / auth ----------
async function login() {
  const username = els.username.value.trim();
  const password = els.password.value;
  if (!username || !password) { els.authState.textContent = 'username + password required'; return; }
  try {
    const body = await rest('/auth/login', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    });
    state.token = body.token || '';
    store.save('token', state.token);
    els.authState.textContent = 'token for ' + (body.username || username) + ' ✓';
  } catch (e) { els.authState.textContent = 'login failed: ' + e.message; }
}

// ---------- project / session picker ----------
async function resolveProject() {
  try {
    const body = await rest('/projects/resolve', {
      method: 'POST', headers: authHeaders(),
      body: JSON.stringify({
        canonical_url: els.canonicalUrl.value.trim(),
        root_commit: els.rootCommit.value.trim(),
        folder_name: els.folderName.value.trim(),
      }),
    });
    state.projectId = body.id || '';
    els.projectId.value = state.projectId;
    store.save('projectId', state.projectId);
    els.projectState.textContent = 'resolved: ' + (body.display_name || body.folder_name || state.projectId);
    refreshMemory();
    refreshEpisodes();
  } catch (e) { els.projectState.textContent = 'resolve failed: ' + e.message; }
}

async function checkActiveWorkspace() {
  const pid = els.projectId.value.trim();
  if (!pid) { els.projectState.textContent = 'set a project ID first'; return; }
  try {
    const ws = await rest('/workspaces/' + encodeURIComponent(pid) + '/active', { headers: authHeaders() });
    els.projectState.textContent = 'active: ' + (ws.machine_id || ws.id) + ' @ ' + (ws.branch || '?') + (ws.is_dirty ? ' (dirty)' : '');
    upsertPresence(ws.user_id || ws.machine_id || ws.id, 'online');
  } catch (e) { els.projectState.textContent = 'no active workspace: ' + e.message; }
}

// ---------- WebSocket ----------
function setWsState(label, cls) {
  els.wsLabel.textContent = label;
  els.wsDot.className = 'dot ' + cls;
}

function connectWs() {
  state.wsWanted = true;
  state.retryMs = 1000;
  openWs();
}

function openWs() {
  if (!state.wsWanted) return;
  let url = els.wsUrl.value.trim();
  if (!url) return;
  // WS auth (issue #130): browsers cannot set headers on WebSocket, so the
  // server accepts ?token= (ws.go serveWS). Append the REST token unless the
  // URL already carries one.
  if (state.token && !/[?&]token=/.test(url)) {
    url += (url.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(state.token);
  }
  try { if (state.ws) state.ws.close(); } catch {}
  setWsState('connecting…', 'dot-idle');
  let ws;
  try { ws = new WebSocket(url); } catch (e) { scheduleRetry(); return; }
  state.ws = ws;
  const t0 = performance.now();

  ws.addEventListener('open', () => {
    setWsState('connected', 'dot-on');
    els.latency.textContent = Math.round(performance.now() - t0) + 'ms to open';
    state.retryMs = 1000;
    sendSubscribe();
    stopPollFallback();
  });
  ws.addEventListener('message', (ev) => {
    // Sub-second render: parse + prepend a node, never a full refresh.
    let msg;
    try { msg = JSON.parse(ev.data); } catch { return; }
    routeWs(msg);
  });
  ws.addEventListener('close', () => {
    setWsState('disconnected', 'dot-off');
    startPollFallback();
    scheduleRetry();
  });
  ws.addEventListener('error', () => { try { ws.close(); } catch {} });
}

function scheduleRetry() {
  if (!state.wsWanted) return;
  setWsState('retry in ' + Math.round(state.retryMs / 1000) + 's (REST still works)', 'dot-idle');
  setTimeout(() => { if (state.wsWanted && (!state.ws || state.ws.readyState > 1)) openWs(); },
    state.retryMs);
  state.retryMs = Math.min(state.retryMs * 2, 30000);
}

function disconnectWs() {
  state.wsWanted = false;
  try { if (state.ws) state.ws.close(); } catch {}
  setWsState('disconnected', 'dot-off');
  stopPollFallback();
}

function sendSubscribe() {
  const pid = els.projectId.value.trim();
  if (!pid || !state.ws || state.ws.readyState !== 1) return;
  state.projectId = pid;
  state.sessionId = els.sessionId.value.trim();
  store.save('projectId', pid);
  store.save('sessionId', state.sessionId);
  state.ws.send(JSON.stringify({
    type: 'subscribe', project_id: pid, session_id: state.sessionId || undefined,
  }));
  els.projectState.textContent = 'subscribed to ' + pid + (state.sessionId ? ' / ' + state.sessionId : '');
}

function sendPresence(status) {
  if (!state.ws || state.ws.readyState !== 1) return;
  state.ws.send(JSON.stringify({ type: 'presence', status }));
}

function routeWs(msg) {
  switch (msg.type) {
    case 'event': onEvent(msg.event); break;
    case 'presence': onPresenceMsg(msg); break;
    case 'memory_update': onMemoryUpdate(msg); break;
    case 'episode_update': onEpisodeUpdate(msg); break;
    case 'subscribed':
      els.projectState.textContent = 'subscribed to ' + (msg.project_id || '?') +
        (msg.session_id ? ' / ' + msg.session_id : '') + ' ✓';
      break;
    case 'error':
      onEvent({ event_type: 'WS_ERROR', payload: { message: msg.message || 'unknown' }, created_at: new Date().toISOString() });
      if (/expired|unauthorized|invalid bearer|401/i.test(msg.message || '')) onUnauthorized();
      break;
    default: onEvent({ event_type: 'UNKNOWN_FRAME', payload: msg, created_at: new Date().toISOString() });
  }
}

// ---------- presence ----------
function upsertPresence(userId, status) {
  if (!userId) return;
  state.presence.set(String(userId), { status, at: Date.now() });
  renderPresence();
}
function onPresenceMsg(msg) {
  upsertPresence(msg.user_id || msg.user || msg.agent_id, msg.status || 'online');
}
function renderPresence() {
  const now = Date.now();
  const items = [...state.presence.entries()].map(([user, p]) => {
    // 90s OfflineThreshold mirrors server.go + §3.4 heartbeat presence.
    const stale = now - p.at > 90000;
    const status = stale ? 'offline' : p.status;
    return { user, status };
  });
  items.sort((a, b) => (a.user < b.user ? -1 : 1));
  els.presenceCount.textContent = '(' + items.length + ')';
  els.presenceList.innerHTML = items.length ? items.map((p) =>
    '<li><span class="dot ' + (p.status === 'online' ? 'dot-on' : p.status === 'typing' ? 'dot-typing' : p.status === 'idle' ? 'dot-idle' : 'dot-off') +
    '"></span><code>' + esc(p.user) + '</code><span class="muted">' + esc(p.status) + '</span></li>'
  ).join('') : '<li class="muted">no presence yet — connect WS and subscribe</li>';
}
setInterval(renderPresence, 15000);

// ---------- live event feed ----------
const HUMAN = ['FILE_MODIFIED', 'COMMAND_EXECUTED', 'GIT_COMMITTED', 'MEMORY_PROPOSED',
  'MEMORY_CONFIRMED', 'EPISODE_OPENED', 'EPISODE_RESOLVED', 'MESSAGE_SENT',
  'SESSION_STARTED', 'CONVERSATION_TURN'];

function eventSummary(ev) {
  const p = (ev && ev.payload) || {};
  switch (ev.event_type) {
    case 'FILE_MODIFIED': return 'modified ' + (p.path || '?');
    case 'FILE_READ': return 'read ' + (p.path || '?');
    case 'COMMAND_EXECUTED': return (p.cmd || p.command || 'cmd') + ' → exit ' + (p.exit_code != null ? p.exit_code : '?');
    case 'GIT_COMMITTED': return (p.message || p.sha || 'commit');
    case 'CONVERSATION_TURN': return (p.speaker || 'agent') + ': ' + String(p.content || '').slice(0, 140);
    case 'MEMORY_PROPOSED': case 'MEMORY_CONFIRMED': case 'MEMORY_REJECTED':
      return (p.key || p.id || 'memory');
    case 'EPISODE_OPENED': case 'EPISODE_RESOLVED': case 'EPISODE_UPDATED':
      return (p.title || p.id || 'episode');
    case 'MESSAGE_SENT': return String(p.text || p.content || '').slice(0, 140);
    default: return Object.keys(p).slice(0, 3).map((k) => k + '=' + JSON.stringify(p[k])).join(' ').slice(0, 140);
  }
}

function onEvent(ev) {
  if (!ev) return;
  const filter = ($('feedFilter').value || '').trim().toLowerCase();
  if (filter && !(String(ev.event_type || '').toLowerCase().includes(filter))) return;
  state.feedTotal += 1;
  els.feedCount.textContent = '(' + state.feedTotal + ')';
  const li = document.createElement('li');
  const interesting = HUMAN.includes(ev.event_type) ? ' hi' : '';
  li.className = 'feed-item' + interesting;
  li.innerHTML = '<span class="badge">' + esc(ev.event_type || '?') + '</span> ' +
    '<span>' + esc(eventSummary(ev)) + '</span> ' +
    '<span class="muted">' + esc(fmtTime(ev.created_at)) +
    (ev.session_id ? ' · ' + esc(ev.session_id.slice(0, 8)) : '') + '</span>';
  els.feed.prepend(li);
  while (els.feed.children.length > 200) els.feed.lastChild.remove();
  // Feed doubles as a presence hint: anyone emitting events is online.
  if (ev.user_id || ev.agent_id) upsertPresence(ev.user_id || ev.agent_id, 'online');
}

// REST fallback while WS is down: re-poll the searchable collections.
// (No GET /events REST route exists yet, so memory/episode search is the pollable surface.)
function startPollFallback() {
  if (state.pollTimer) return;
  state.pollTimer = setInterval(() => {
    if (state.ws && state.ws.readyState === 1) return;
    if (els.projectId.value.trim() && state.token) { refreshMemory(true); refreshEpisodes(true); }
  }, 5000);
}
function stopPollFallback() {
  clearInterval(state.pollTimer);
  state.pollTimer = null;
}

// ---------- memory explorer ----------
function memoryCard(item) {
  const li = document.createElement('li');
  li.className = 'mini-card';
  li.dataset.id = item.id || '';
  li.innerHTML =
    '<div class="mini-head"><code>' + esc(item.key || item.id) + '</code>' +
    '<span class="badge">' + esc(item.status || '') + '</span>' +
    '<span class="badge dim">' + esc(item.level || '') + '</span></div>' +
    '<p>' + esc(item.content || '') + '</p>' +
    (item.context_snippet ? '<p class="muted">◷ ' + esc(item.context_snippet) + '</p>' : '') +
    '<div class="row">' +
    '<button data-act="confirm" type="button">Confirm</button>' +
    '<button data-act="reject" type="button" class="ghost">Reject</button>' +
    '<button data-act="promote" type="button" class="ghost">Promote → project</button>' +
    '<span class="muted">conf ' + esc(item.confidence != null ? item.confidence : '?') + '</span></div>';
  li.querySelectorAll('button').forEach((b) =>
    b.addEventListener('click', () => memoryAction(item, b.dataset.act, li)));
  return li;
}

// Best-effort confirmation flow (plan §2.8). The server implements
// POST /memory/:id/confirm|reject|promote (routes_extra.go + promote.go).
// UI state flips ONLY on success (issue #145): flipping on failure left
// the dashboard diverged from the server with only a hint as evidence.
async function memoryAction(item, act, node) {
  const map = { confirm: 'confirm', reject: 'reject', promote: 'promote' };
  const endpoint = '/memory/' + encodeURIComponent(item.id) + '/' + map[act];
  try {
    await rest(endpoint, { method: 'POST', headers: authHeaders(), body: JSON.stringify({}) });
  } catch (e) {
    const hint = node.querySelector('.muted');
    if (hint) hint.textContent = endpoint + ' failed (' + e.message + ') — not applied';
    return;
  }
  if (act === 'confirm') item.status = 'CONFIRMED';
  if (act === 'reject') item.status = 'REJECTED';
  if (act === 'promote') { item.level = 'project'; item.session_id = ''; }
  if (state.ws && state.ws.readyState === 1) {
    try {
      state.ws.send(JSON.stringify({
        type: 'action',
        event_type: act === 'reject' ? 'MEMORY_REJECTED' : act === 'promote' ? 'MEMORY_PROMOTED' : 'MEMORY_CONFIRMED',
        payload: { id: item.id, key: item.key },
      }));
    } catch {}
  }
  const fresh = memoryCard(item);
  node.replaceWith(fresh);
}

function clientFilterMemories(items) {
  const lvl = els.memLevel.value, st = els.memStatus.value;
  return items.filter((m) =>
    (!lvl || (m.level || '') === lvl) && (!st || (m.status || '') === st));
}

async function refreshMemory(quiet) {
  const pid = els.projectId.value.trim();
  if (!pid) return;
  try {
    const body = await rest('/memory/search?project_id=' + encodeURIComponent(pid) + '&limit=50',
      { headers: authHeaders() });
    const items = clientFilterMemories(body.items || []);
    els.memoryList.innerHTML = '';
    items.forEach((m) => els.memoryList.appendChild(memoryCard(m)));
    if (!items.length && !quiet) els.memoryList.innerHTML = '<li class="muted">no memories match</li>';
  } catch (e) { if (!quiet) els.memoryList.innerHTML = '<li class="muted">search failed: ' + esc(e.message) + '</li>'; }
}

function onMemoryUpdate(msg) {
  // Live apply without refresh: prepend/update the card in place.
  if (!msg.item) return;
  const list = els.memoryList;
  const existing = msg.item.id ? list.querySelector('[data-id="' + CSS.escape(msg.item.id) + '"]') : null;
  const card = memoryCard(msg.item);
  if (existing) existing.replaceWith(card);
  else list.prepend(card);
  onEvent({ event_type: msg.action === 'rejected' ? 'MEMORY_REJECTED' : 'MEMORY_CONFIRMED', payload: { key: msg.item.key }, created_at: new Date().toISOString() });
}

// ---------- episode inspector ----------
function episodeCard(ep) {
  const div = document.createElement('div');
  div.className = 'mini-card';
  if (ep.id) div.dataset.id = ep.id;
  const arc = [['Trigger', ep.trigger], ['Investigation', ep.investigation],
    ['Root cause', ep.root_cause], ['Fix', ep.resolution], ['Verification', ep.verification]]
    .filter(([, v]) => v).map(([k, v]) =>
      '<details><summary>' + esc(k) + '</summary><p>' + esc(v) + '</p></details>').join('');
  div.innerHTML =
    '<div class="mini-head"><strong>' + esc(ep.title || ep.id) + '</strong>' +
    '<span class="badge">' + esc(ep.status || '') + '</span>' +
    '<span class="badge dim">' + esc(ep.episode_type || '') + '</span></div>' +
    (ep.error_patterns && ep.error_patterns.length ? '<p><code>' + esc(ep.error_patterns.join(', ')) + '</code></p>' : '') +
    (ep.files_involved && ep.files_involved.length ? '<p class="muted">files: ' + esc(ep.files_involved.join(', ')) + '</p>' : '') +
    arc;
  return div;
}

async function refreshEpisodes(quiet) {
  const pid = els.projectId.value.trim();
  if (!pid) return;
  try {
    const body = await rest('/episodes/search?project_id=' + encodeURIComponent(pid) + '&limit=50',
      { headers: authHeaders() });
    const eps = body.items || [];
    els.episodeList.innerHTML = '';
    eps.forEach((e) => els.episodeList.appendChild(episodeCard(e)));
    if (!eps.length && !quiet) els.episodeList.innerHTML = '<p class="muted">no episodes yet</p>';
  } catch (e) { if (!quiet) els.episodeList.innerHTML = '<p class="muted">search failed: ' + esc(e.message) + '</p>'; }
}

function onEpisodeUpdate(msg) {
  if (!msg.episode) return;
  const existing = msg.episode.id ? els.episodeList.querySelector('[data-id="' + CSS.escape(msg.episode.id) + '"]') : null;
  const card = episodeCard(msg.episode);
  if (existing) existing.replaceWith(card);
  else els.episodeList.prepend(card);
  onEvent({ event_type: msg.action === 'resolved' ? 'EPISODE_RESOLVED' : 'EPISODE_OPENED', payload: { title: msg.episode.title }, created_at: new Date().toISOString() });
}

// ---------- search ----------
async function runSearch() {
  const pid = els.projectId.value.trim();
  const q = els.searchBox.value.trim();
  if (!pid) { els.memSearchCount.textContent = '(set a project ID first)'; return; }
  const [mem, eps] = await Promise.all([
    rest('/memory/search?project_id=' + encodeURIComponent(pid) + '&q=' + encodeURIComponent(q) + '&limit=20', { headers: authHeaders() }).catch((e) => ({ error: e.message })),
    // error_pattern enables the server's exact-match path (issue #136);
    // q covers the narrative fallback. Both fire from one box.
    rest('/episodes/search?project_id=' + encodeURIComponent(pid) + '&q=' + encodeURIComponent(q) + '&error_pattern=' + encodeURIComponent(q) + '&limit=20', { headers: authHeaders() }).catch((e) => ({ error: e.message })),
  ]);
  els.memSearchResults.innerHTML = '';
  els.epSearchResults.innerHTML = '';
  if (mem.error) els.memSearchCount.textContent = '(' + mem.error + ')';
  else {
    els.memSearchCount.textContent = '(' + (mem.count != null ? mem.count : (mem.items || []).length) + ')';
    (mem.items || []).forEach((m) => els.memSearchResults.appendChild(memoryCard(m)));
  }
  if (eps.error) els.epSearchCount.textContent = '(' + eps.error + ')';
  else {
    els.epSearchCount.textContent = '(' + (eps.count != null ? eps.count : (eps.items || []).length) + ')';
    (eps.items || []).forEach((e) => els.epSearchResults.appendChild(episodeCard(e)));
  }
}

// ---------- wiring ----------
function init() {
  // Same-origin defaults unless the user stored an override (#130). Empty
  // inputs (fresh clone served from any host) derive from window.location.
  const storedApi = store.load('apiBase', '');
  const storedWs = store.load('wsUrl', '');
  els.apiBase.value = storedApi || els.apiBase.value.trim() || defaultApiBase();
  els.wsUrl.value = storedWs || els.wsUrl.value.trim() || defaultWsUrl();
  els.projectId.value = state.projectId;
  els.sessionId.value = state.sessionId;
  els.agentName.value = store.load('agentName', '');
  if (state.token) els.authState.textContent = 'restored token ✓';
  renderPresence();

  $('loginBtn').addEventListener('click', login);
  $('wsBtn').addEventListener('click', () => {
    store.save('apiBase', apiBase());
    store.save('wsUrl', els.wsUrl.value.trim());
    store.save('agentName', els.agentName.value.trim());
    connectWs();
  });
  $('wsDisconnectBtn').addEventListener('click', disconnectWs);
  $('resolveBtn').addEventListener('click', resolveProject);
  $('subscribeBtn').addEventListener('click', sendSubscribe);
  $('activeWsBtn').addEventListener('click', checkActiveWorkspace);
  $('typingBtn').addEventListener('click', () => sendPresence('typing'));
  $('idleBtn').addEventListener('click', () => sendPresence('idle'));
  $('clearFeedBtn').addEventListener('click', () => {
    els.feed.innerHTML = ''; state.feedTotal = 0; els.feedCount.textContent = '';
  });
  $('memRefreshBtn').addEventListener('click', () => refreshMemory());
  $('epRefreshBtn').addEventListener('click', () => refreshEpisodes());
  $('searchBtn').addEventListener('click', runSearch);
  els.searchBox.addEventListener('keydown', (e) => { if (e.key === 'Enter') runSearch(); });
  els.memLevel.addEventListener('change', () => refreshMemory(true));
  els.memStatus.addEventListener('change', () => refreshMemory(true));
  els.projectId.addEventListener('change', () => {
    state.projectId = els.projectId.value.trim();
    store.save('projectId', state.projectId);
  });
  els.sessionId.addEventListener('change', () => {
    state.sessionId = els.sessionId.value.trim();
    store.save('sessionId', state.sessionId);
  });
}

document.addEventListener('DOMContentLoaded', init);

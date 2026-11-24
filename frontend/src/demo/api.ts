/*
 * The demo's in-browser API. It answers the same paths as the Nexus server
 * from src/demo/data.ts, keeps edits in memory for this tab only, and refuses
 * anything that would need a real account. Nothing here touches the network.
 */
import type { MemoryItem } from '@/types/api'
import {
  CHATS,
  CHECKOUT,
  EVENTS,
  MEMORIES,
  ME,
  ORG,
  ORG_ID,
  ORG_MEMBERS,
  PROJECTS,
  USERS,
  ago,
  chatSession,
  chatTurns,
  dashboard,
} from './data'

type Req = { method: string; path: string; query: URLSearchParams; body: unknown }
type Res = { status: number; body?: unknown }

const ok = (body: unknown): Res => ({ status: 200, body })
const list = <T,>(items: T[]) => ok({ items, count: items.length, total: items.length, has_more: false })
const notHere = (): Res => ({
  status: 403,
  body: { error: { code: 403, message: 'This is the demo, so nothing is saved to a server. Create a free account to do this for real.' } },
})
const missing = (): Res => ({ status: 404, body: { error: { code: 404, message: 'Not found in the demo workspace' } } })

const memories: MemoryItem[] = MEMORIES.map((m) => ({ ...m }))
const history = new Map<string, { version: number; key: string; content: string; created_at: string; edited_by?: string }[]>()
const people = Object.values(USERS)

function findMemory(id: string) {
  return memories.find((m) => m.id === id)
}

function searchMemories(q: URLSearchParams) {
  const project = q.get('project_id')
  const text = (q.get('q') ?? '').toLowerCase().trim()
  const level = q.get('level')
  const tags = (q.get('tags') ?? '').split(',').filter(Boolean)
  const limit = Number(q.get('limit') ?? 50)
  const offset = Number(q.get('offset') ?? 0)
  let rows = memories.filter((m) => (!project || m.project_id === project) && m.status !== 'REJECTED')
  if (level) rows = rows.filter((m) => m.level === level || m.scope === level)
  if (tags.length) rows = rows.filter((m) => tags.every((t) => m.tags?.includes(t) || m.category === t))
  if (text) {
    const words = text.split(/\s+/)
    const score = (m: MemoryItem) => {
      const hay = `${m.key} ${m.content} ${(m.tags ?? []).join(' ')} ${m.category ?? ''}`.toLowerCase()
      return words.filter((w) => hay.includes(w)).length
    }
    rows = rows.map((m) => [m, score(m)] as const).filter(([, s]) => s > 0).sort((a, b) => b[1] - a[1]).map(([m]) => m)
  } else {
    rows = [...rows].sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))
  }
  return ok({ items: rows.slice(offset, offset + limit), count: Math.min(limit, rows.length - offset), total: rows.length, has_more: offset + limit < rows.length })
}

function setStatus(id: string, status: string) {
  const m = findMemory(id)
  if (!m) return missing()
  m.status = status
  m.updated_at = new Date().toISOString()
  if (status === 'CONFIRMED') m.confirmed_by = ME.id
  return ok(m)
}

function timeline(q: URLSearchParams) {
  const project = q.get('project_id')
  const text = (q.get('q') ?? '').toLowerCase().trim()
  let rows = CHATS.filter((c) => !project || c.card.project_id === project)
  if (text) rows = rows.filter((c) => `${c.card.title} ${c.summary} ${c.turns.map((t) => t.text ?? '').join(' ')}`.toLowerCase().includes(text))
  return ok({ items: rows.map((c) => c.card) })
}

const chatById = (id: string) => CHATS.find((c) => c.card.session_id === decodeURIComponent(id))

const billingPlan = {
  id: 'free',
  name: 'Free',
  description: 'For individuals and small teams trying Nexus.',
  price_cents: 0,
  currency: 'usd',
  interval: 'month',
  public: true,
  rank: 0,
  limits: { orgs: 1, projects: 3, members: 5, memories: 2000, storage_bytes: 1 << 30, github_import: true },
  features: ['Shared project memory', 'Six agent harnesses', 'Session sharing'],
}

/** GET routes: [pattern, handler]. Patterns are matched against the path without the API prefix. */
const GET: [RegExp, (m: RegExpMatchArray, r: Req) => Res][] = [
  [/^\/users\/me$/, () => ok({ id: ME.id, username: ME.username, email: ME.email, created_at: ago(70 * 86400000), settings: {} })],
  [/^\/users\/me\/usage$/, () => ok({ memories_created: 128, episodes_created: 42, requests_approx: 2913 })],
  [/^\/auth\/tokens$/, () => list([{ id: 'tok-1', user_id: ME.id, name: 'Nexus Desktop (MAYA-LAPTOP)', token_prefix: 'nx_4f9a', scopes: [], created_at: ago(30 * 86400000), last_used_at: ago(25 * 60000) }])],
  [/^\/projects$/, () => list(PROJECTS)],
  [/^\/projects\/([^/]+)\/dashboard$/, (m) => ok(dashboard(m[1]))],
  [/^\/projects\/([^/]+)\/events$/, (m, r) => {
    const type = r.query.get('type')
    const user = r.query.get('user_id')
    return list(EVENTS.filter((e) => e.project_id === m[1] && (!type || e.event_type === type) && (!user || e.user_id === user)))
  }],
  [/^\/projects\/([^/]+)\/members$/, () => list([{ user_id: ME.id, role: 'owner' }, { user_id: USERS.priya.id, role: 'admin' }, { user_id: USERS.daniel.id, role: 'member' }])],
  [/^\/projects\/([^/]+)\/roles$/, (m) => list([
    { id: 'owner', project_id: m[1], name: 'owner', description: 'Full control, including billing and deleting the project.', is_builtin: true, permissions: ['*'] },
    { id: 'admin', project_id: m[1], name: 'admin', description: 'Manage members, memories and integrations.', is_builtin: true, permissions: ['members:write', 'memory:write'] },
    { id: 'member', project_id: m[1], name: 'member', description: 'Read everything, propose and edit memories.', is_builtin: true, permissions: ['memory:write'] },
  ])],
  [/^\/projects\/([^/]+)\/presence$/, () => list([{ user_id: ME.id, status: 'online' }, { user_id: USERS.priya.id, status: 'online' }, { user_id: USERS.daniel.id, status: 'idle' }])],
  [/^\/projects\/([^/]+)\/github\/status$/, (m) => ok({ connected: true, project_id: m[1], owner: 'acme', repo: m[1] === CHECKOUT ? 'checkout-service' : 'storefront-web', html_url: 'https://github.com/', sync_mode: 'members', connected_by: ME.id, connected_at: ago(40 * 86400000), last_import_at: ago(3 * 86400000), has_token: true, oauth_configured: true, oauth_linked: true })],
  [/^\/projects\/([^/]+)\/agents$/, (m) => list(['claude', 'codex', 'cursor', 'opencode', 'gemini', 'kimi'].map((a) => ({ project_id: m[1], agent_id: a, mode: a === 'gemini' ? 'propose_only' : 'full', rate_limit: 120, tools: { memory_search: true, project_knowledge: true, related_to: true, file_history: true, why: true, line_history: true, memory_propose: true }, updated_at: ago(6 * 86400000) })))],
  [/^\/projects\/([^/]+)\/mcp\/tool-calls$/, (m) => list(EVENTS.filter((e) => e.project_id === m[1] && e.event_type === 'MCP_TOOL_CALL').map((e) => ({ id: e.id, event_type: e.event_type, agent_id: e.agent_id, tool_name: String(e.payload?.tool_name ?? ''), duration_ms: 38, created_at: e.created_at, user_id: e.user_id, arguments: { query: 'capture retry' } })))],
  [/^\/projects\/([^/]+)\/snapshots$/, () => list([])],
  [/^\/workspaces\/([^/]+)\/active$/, () => list([{ id: 'ws-1', machine_id: 'MAYA-LAPTOP', path: 'D:\\code\\checkout-service', user_id: ME.id, branch: 'retry-backoff', last_seen: ago(60000), is_online: true }])],
  [/^\/notifications\/subscribe$/, () => ok({ subscribed: false })],
  [/^\/workspaces\/([^/]+)\/local$/, (m) => ok({ online: false, project_id: m[1] })],
  [/^\/memory\/search$/, (_m, r) => searchMemories(r.query)],
  [/^\/memory\/harvest$/, () => ok({ items: [], count: 0, counts: { queued: 0, processing: 0, done: 14, failed: 0, total: 14 }, total: 14 })],
  [/^\/memory\/([^/]+)\/history$/, (m) => {
    const item = findMemory(m[1])
    if (!item) return missing()
    const past = history.get(item.id) ?? []
    const current = { version: past.length + 1, key: item.key, content: item.content, created_at: item.updated_at, edited_by: ME.id }
    return ok({ current, items: [current, ...past].map((v) => ({ ...v, memory_id: item.id })) })
  }],
  [/^\/memory\/([^/]+)\/shares$/, () => list([])],
  [/^\/v1\/timeline$/, (_m, r) => timeline(r.query)],
  [/^\/v1\/agent-sessions\/([^/]+)$/, (m) => { const c = chatById(m[1]); return c ? ok(chatSession(c)) : missing() }],
  [/^\/v1\/agent-sessions\/([^/]+)\/fetch$/, (m) => { const c = chatById(m[1]); return c ? ok({ items: chatTurns(c) }) : missing() }],
  [/^\/v1\/agent-sessions\/([^/]+)\/summary$/, (m) => ok({ summary: chatById(m[1])?.summary ?? '' })],
  [/^\/v1\/agent-sessions\/([^/]+)\/memories$/, (m) => {
    const c = chatById(m[1])
    return ok({ items: c ? memories.filter((x) => x.project_id === c.card.project_id).slice(1, 4) : [] })
  }],
  [/^\/v1\/sharing\/recipients$/, () => ok({ people: people.map((p) => ({ user_id: p.id, name: p.name, project_role: p.id === ME.id ? 'owner' : 'member', organization_role: 'member' })) })],
  [/^\/v1\/desktop\/devices$/, () => list([])],
  [/^\/v1\/teleports\/inbox$/, () => list([{ id: 'tp-1', session_id: CHATS[7].card.session_id, from_user_id: USERS.priya.id, to_user_id: ME.id, status: 'pending', preview: 'Explain the payment state machine', created_at: ago(3 * 3600000) }])],
  [/^\/v1\/teleports\/sent$/, () => list([])],
  [/^\/v1\/storage\/usage$/, () => ok({ scope: 'user', used: 312 * 1024 * 1024, cap: 1 << 30, state: 'ok' })],
  [/^\/sessions$/, (_m, r) => list([
    { id: 'b7e2c1d0-1111-4a2b-9c3d-4e5f6a7b8c9d', project_id: r.query.get('project_id') ?? CHECKOUT, title: 'Capture retries: review and hand-off', created_by: ME.id, is_active: true, created_at: ago(5 * 3600000) },
    { id: 'b7e2c1d0-2222-4a2b-9c3d-4e5f6a7b8c9d', project_id: r.query.get('project_id') ?? CHECKOUT, title: 'v2 orders migration dry run', created_by: USERS.priya.id, is_active: false, created_at: ago(2 * 86400000), ended_at: ago(2 * 86400000 - 3 * 3600000) },
  ])],
  [/^\/orgs$/, () => list([ORG])],
  [/^\/orgs\/([^/]+)$/, () => ok({ organization: ORG, members: ORG_MEMBERS, projects: PROJECTS })],
  [/^\/orgs\/([^/]+)\/storage$/, () => ok({
    members: people.map((p, i) => ({ user_id: p.id, session_count: [38, 21, 9][i], last_active_at: ago((i + 1) * 3600000) })),
    projects: PROJECTS.map((p, i) => ({ project_id: p.id, folder_name: p.folder_name ?? '', display_name: p.display_name, capture_enabled: true, session_count: [52, 16][i], storage_bytes: [214, 61][i] * 1024 * 1024 })),
  })],
  [/^\/orgs\/([^/]+)\/audit$/, () => list([
    { at: ago(4 * 3600000), actor_user_id: ME.id, action: 'session.share', resource_kind: 'session', outcome: 'ok' },
    { at: ago(3 * 86400000), actor_user_id: USERS.priya.id, action: 'member.role', resource_kind: 'member', outcome: 'ok' },
    { at: ago(30 * 86400000), actor_user_id: USERS.priya.id, action: 'member.add', resource_kind: 'member', outcome: 'ok' },
  ])],
  [/^\/orgs\/([^/]+)\/shares$/, () => list([{ session_id: CHATS[0].card.session_id, project_id: CHECKOUT, owner_id: ME.id, grantee_id: USERS.priya.id, live: true, created_at: ago(4 * 3600000) }])],
  [/^\/orgs\/([^/]+)\/billing$/, () => ok(billingSnapshot('org', ORG_ID))],
  [/^\/branches$/, (_m, r) => list([
    { id: 'br-main', project_id: r.query.get('project_id') ?? CHECKOUT, name: 'main', owner_id: ME.id, visibility: 'project', created_at: ago(60 * 86400000) },
    { id: 'br-v2', project_id: r.query.get('project_id') ?? CHECKOUT, name: 'orders-v2', owner_id: USERS.priya.id, parent_branch_id: 'br-main', visibility: 'project', created_at: ago(2 * 86400000) },
  ])],
  [/^\/branches\/diff$/, (_m, r) => ok({
    project_id: r.query.get('project_id') ?? CHECKOUT, source: r.query.get('source') ?? 'orders-v2', source_id: 'br-v2', target: r.query.get('target') ?? 'main', target_id: 'br-main',
    added: [{ key: 'orders/v2-migration', content: memories.find((m) => m.key === 'orders/v2-migration')?.content ?? '' }],
    removed: [],
    modified: [{ key: 'project/brief', old_content: 'Stack: TypeScript, Postgres 15.', new_content: 'Stack: TypeScript, Postgres 16.' }],
    unchanged: memories.filter((m) => m.project_id === CHECKOUT).slice(1, 6).map((m) => ({ key: m.key, content: m.content })),
  })],
  [/^\/notifications$/, () => list([
    { id: 'n1', type: 'memory_proposed', title: 'Gemini proposed a memory', body: 'orders/v2-migration is waiting for review.', project_id: CHECKOUT, created_at: ago(22 * 3600000), href: '/app/memory' },
    { id: 'n2', type: 'session_shared', title: 'Priya shared a chat with you', body: 'Explain the payment state machine', project_id: CHECKOUT, created_at: ago(6 * 86400000), href: '/app/chats' },
    { id: 'n3', type: 'memory_stale', title: 'A memory may be stale', body: 'webhooks/signature-tolerance: src/webhooks/verify.ts changed.', project_id: CHECKOUT, created_at: ago(9 * 86400000), href: '/app/memory' },
  ])],
  [/^\/notifications\/vapid-public-key$/, () => ok({ configured: false, public_key: '' })],
  [/^\/notifications\/devices$/, () => list([])],
  [/^\/notifications\/preferences$/, () => ok({ types: { memory_proposed: true, session_shared: true, memory_stale: true, handoff: true } })],
  [/^\/billing\/plans$/, () => list([billingPlan, { ...billingPlan, id: 'team', name: 'Team', price_cents: 1200, rank: 1, description: 'For teams that share memory every day.', limits: { orgs: 3, projects: 25, members: 25, memories: 50000, storage_bytes: 20 * (1 << 30), github_import: true } }])],
  [/^\/billing\/subscription$/, () => ok(billingSnapshot('user', ME.id))],
  [/^\/platform\/version$/, () => ok({ version: 'demo', commit: 'demo' })],
  [/^\/platform\/releases\/latest$/, () => missing()],
]

function billingSnapshot(owner_type: string, owner_id: string) {
  return {
    subscription: { id: 'sub-demo', owner_type, owner_id, plan_id: 'free', status: 'active', provider: 'none', current_period_start: ago(10 * 86400000), current_period_end: ago(-20 * 86400000), created_at: ago(70 * 86400000), updated_at: ago(10 * 86400000) },
    plan: billingPlan,
    usage: { orgs: 1, projects: 2, members: 3, memories: memories.length },
    checkout: { provider: 'none', url: null },
  }
}

/** Mutations that make sense to try in the demo; they change this tab's copy only. */
const WRITE: [string, RegExp, (m: RegExpMatchArray, r: Req) => Res][] = [
  ['POST', /^\/memory\/([^/]+)\/confirm$/, (m) => setStatus(m[1], 'CONFIRMED')],
  ['POST', /^\/memory\/([^/]+)\/reject$/, (m) => setStatus(m[1], 'REJECTED')],
  ['POST', /^\/memory\/([^/]+)\/promote$/, (m) => {
    const item = findMemory(m[1])
    if (!item) return missing()
    item.level = 'team'
    item.scope = 'team'
    return ok(item)
  }],
  ['PUT', /^\/memory\/([^/]+)$/, (m, r) => {
    const item = findMemory(m[1])
    if (!item) return missing()
    const past = history.get(item.id) ?? []
    past.unshift({ version: past.length + 1, key: item.key, content: item.content, created_at: item.updated_at, edited_by: item.user_id })
    history.set(item.id, past)
    Object.assign(item, r.body as Partial<MemoryItem>, { updated_at: new Date().toISOString() })
    return ok(item)
  }],
  ['POST', /^\/memory$/, (_m, r) => {
    const body = (r.body ?? {}) as Partial<MemoryItem>
    const item: MemoryItem = {
      id: crypto.randomUUID(), project_id: body.project_id ?? CHECKOUT, user_id: ME.id, key: body.key ?? 'note', content: body.content ?? '',
      level: body.level ?? 'project', scope: body.scope ?? 'project', tags: body.tags ?? [], confidence: 1, status: 'CONFIRMED', use_count: 0,
      created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
    }
    memories.unshift(item)
    return ok(item)
  }],
  ['POST', /^\/notifications\/read$/, () => ok({ ok: true })],
  ['POST', /^\/sessions\/([^/]+)\/steer\/(prompt|interrupt|resume)$/, (m) => ok({ session_id: m[1], state: m[2] === 'interrupt' ? 'pause_requested' : 'running' })],
  ['POST', /^\/v1\/teleports\/([^/]+)\/accept$/, (m) => ok({ id: m[1], status: 'accepted' })],
]

export function handle(req: Req): Res {
  if (req.method === 'GET' || req.method === 'HEAD') {
    for (const [re, fn] of GET) {
      const m = req.path.match(re)
      if (m) return fn(m, req)
    }
    // Lists the demo has no data for read as empty rather than failing.
    return /s$/.test(req.path) ? list([]) : missing()
  }
  for (const [method, re, fn] of WRITE) {
    const m = req.method === method && req.path.match(re)
    if (m) return fn(m, req)
  }
  return notHere()
}

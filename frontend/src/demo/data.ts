/*
 * Sample workspace for the /demo route. Everything here is fictional: the
 * people (Maya Chen, Priya Raman, Daniel Okafor), the Acme organisation and
 * its two projects. Times are relative to when the demo opens, so the data
 * always looks recent.
 */
import type { ChatCard, ChatSession, ChatTurn } from '@/api/chats'
import type { ProjectDashboard } from '@/api/dashboard'
import type { MemoryItem, Organization, Project, ProjectEvent } from '@/types/api'

const NOW = Date.now()
const MIN = 60_000
const HOUR = 60 * MIN
const DAY = 24 * HOUR
export const ago = (ms: number) => new Date(NOW - ms).toISOString()

export const ORG_ID = '7d0c2a8e-41b6-4e7a-9c55-0a2f6b1d9e31'
export const USERS = {
  maya: { id: '3f6c1e2a-8b4d-4f7e-a1c9-5d2e8f0b7a61', name: 'Maya Chen', username: 'maya', email: 'maya@acme.example' },
  priya: { id: '9a2b4c6d-1e3f-4a5b-8c7d-0e1f2a3b4c5d', name: 'Priya Raman', username: 'priya', email: 'priya@acme.example' },
  daniel: { id: '5e8f7a6b-2c1d-4e3f-9a8b-7c6d5e4f3a2b', name: 'Daniel Okafor', username: 'daniel', email: 'daniel@acme.example' },
}
export const ME = USERS.maya

export const PROJECTS: Project[] = [
  { id: 'a0f5c266-de47-480f-b37e-a0ad32e2485f', folder_name: 'checkout-service', display_name: 'checkout-service', canonical_url: 'github.com/acme/checkout-service', org_id: ORG_ID, created_by: ME.id, created_at: ago(64 * DAY) },
  { id: 'ced19b43-870c-47e1-8a99-5d5313d51b90', folder_name: 'storefront-web', display_name: 'storefront-web', canonical_url: 'github.com/acme/storefront-web', org_id: ORG_ID, created_by: USERS.priya.id, created_at: ago(41 * DAY) },
]
export const [CHECKOUT, STOREFRONT] = PROJECTS.map((p) => p.id)

export const ORG: Organization = { id: ORG_ID, name: 'Acme', slug: 'acme', created_by: ME.id, created_at: ago(70 * DAY) }
export const ORG_MEMBERS = [
  { org_id: ORG_ID, user_id: ME.id, role: 'ADMIN', created_at: ago(70 * DAY) },
  { org_id: ORG_ID, user_id: USERS.priya.id, role: 'ADMIN', granted_by: ME.id, created_at: ago(60 * DAY) },
  { org_id: ORG_ID, user_id: USERS.daniel.id, role: 'MEMBER', granted_by: USERS.priya.id, created_at: ago(30 * DAY) },
]

/* ---------- memory ---------- */

type Mem = Partial<MemoryItem> & Pick<MemoryItem, 'key' | 'content'>
let memSeq = 0
function mem(project: string, m: Mem, ageMs: number): MemoryItem {
  memSeq += 1
  return {
    id: `0d${String(memSeq).padStart(6, '0')}-6c1a-4b7e-9f2d-${String(memSeq).padStart(12, '0')}`,
    project_id: project,
    user_id: ME.id,
    level: 'project',
    scope: 'project',
    confidence: 0.9,
    status: 'CONFIRMED',
    source: 'agent',
    use_count: 3,
    visibility: 'project',
    outcome: 'active',
    created_at: ago(ageMs),
    updated_at: ago(ageMs / 2),
    ...m,
  }
}

export const MEMORIES: MemoryItem[] = [
  mem(CHECKOUT, {
    key: 'project/brief',
    category: 'architecture',
    tags: ['brief'],
    use_count: 41,
    content:
      '# Project brief: checkout-service\nWhat it is: the Go + TypeScript service that turns a cart into a paid order. It talks to the payment provider, writes the ledger and emits order events.\nStack: TypeScript (Node 22) for the API, Go 1.24 for the ledger worker, Postgres 16.\n\n## Key decisions\n- Card captures retry 429 and 5xx with exponential backoff and full jitter (base 200 ms, cap 2 s).\n- One Idempotency-Key per capture; never retry a declined card (402).\n- Money is integer minor units everywhere; never floats.',
  }, 5 * MIN),
  mem(CHECKOUT, {
    key: 'payments/capture-retries',
    category: 'architecture',
    tags: ['payments', 'retry'],
    files_affected: ['src/payments/retry.ts', 'src/payments/capture.ts'],
    tools_used: ['memory_search', 'why'],
    use_count: 17,
    proposed_by: 'claude',
    content: 'Captures retry 429 and 5xx responses with exponential backoff and full jitter: 200 ms base, capped at 2 s, CAPTURE_RETRY_ATTEMPTS attempts (default 3). Declined cards (402) are never retried.',
    context_snippet: 'Decided in “Retry failed card captures with backoff” after a provider outage failed whole checkouts.',
  }, 2 * HOUR),
  mem(CHECKOUT, {
    key: 'payments/idempotency-key',
    category: 'api',
    tags: ['payments', 'idempotency'],
    files_affected: ['src/payments/capture.ts'],
    use_count: 12,
    content: 'capturePayment() creates exactly one Idempotency-Key per capture (line 6) and sends it on every retry. The provider deduplicates the same key for 24 hours, so retrying inside withRetry() is safe.',
  }, 2 * HOUR),
  mem(CHECKOUT, {
    key: 'conventions/money-minor-units',
    category: 'conventions',
    tags: ['money'],
    use_count: 23,
    content: 'All amounts are integers in minor units (cents). Format only at the edge with formatMoney(amount, currency). Never use floats for money, including in tests.',
  }, 12 * DAY),
  mem(CHECKOUT, {
    key: 'webhooks/signature-tolerance',
    category: 'auth',
    tags: ['webhooks', 'stale'],
    files_affected: ['src/webhooks/verify.ts'],
    use_count: 6,
    outcome: 'active',
    content: 'Webhook signatures are accepted within a 300 s clock tolerance (WEBHOOK_TOLERANCE_SECONDS). verify.ts changed since this was written, so check it before relying on the number.',
  }, 9 * DAY),
  mem(CHECKOUT, {
    key: 'refunds/partial-captures',
    category: 'architecture',
    tags: ['refunds'],
    files_affected: ['src/refunds/partial.ts'],
    use_count: 4,
    proposed_by: 'claude',
    content: 'A refund on a partially captured payment must not exceed the captured amount. The guard lives in refunds/partial.ts and returns 409 with code refund_exceeds_capture.',
  }, 4 * DAY),
  mem(CHECKOUT, {
    key: 'infra/ledger-worker-go',
    category: 'infrastructure',
    tags: ['go', 'ledger'],
    use_count: 5,
    proposed_by: 'kimi',
    content: 'The ledger worker builds with Go 1.24 (toolchain pinned in go.mod). CI uses the same image as production: ghcr.io/acme/ledger-build:1.24.',
  }, 3 * DAY),
  mem(CHECKOUT, {
    key: 'observability/retry-logging',
    category: 'conventions',
    tags: ['logging', 'retry'],
    files_affected: ['src/payments/retry.ts'],
    use_count: 3,
    proposed_by: 'claude',
    content: 'Each capture retry logs a warn line with attempt number and provider status. The last attempt rethrows instead of sleeping again.',
  }, 90 * MIN),
  mem(CHECKOUT, {
    key: 'process/review-payments-changes',
    category: 'process',
    tags: ['review'],
    level: 'team',
    scope: 'team',
    use_count: 8,
    user_id: USERS.priya.id,
    content: 'Changes under src/payments need a review from Priya or Daniel and a run of the payments-reviewer sub-agent before merge.',
  }, 20 * DAY),
  mem(CHECKOUT, {
    key: 'orders/v2-migration',
    category: 'dependencies',
    tags: ['orders', 'migration'],
    status: 'PROPOSED',
    confidence: 0.72,
    use_count: 0,
    proposed_by: 'gemini',
    content: 'The v2 orders migration backfills order_lines in batches of 5,000 and must run before the API switches to the v2 schema. Rollback drops order_lines_v2 only.',
    context_snippet: 'Proposed by Gemini from “Draft the v2 orders migration notes”.',
  }, 22 * HOUR),
  mem(CHECKOUT, {
    key: 'exports/orders-csv',
    category: 'api',
    tags: ['exports'],
    status: 'PROPOSED',
    confidence: 0.66,
    use_count: 0,
    proposed_by: 'codex',
    content: 'GET /orders/export streams CSV with a header row and RFC 4180 quoting. Amounts are exported in minor units with a currency column.',
  }, 5 * DAY),
  mem(CHECKOUT, {
    key: 'personal/sandbox-notes',
    category: 'conventions',
    level: 'personal',
    scope: 'personal',
    visibility: 'private',
    tags: ['sandbox'],
    use_count: 2,
    content: 'My sandbox keys live in .env (git-ignored). .env.example lists the names with empty values. Rotate the sandbox payments key after pasting it anywhere.',
  }, 4 * DAY),
  mem(STOREFRONT, {
    key: 'project/brief',
    category: 'architecture',
    tags: ['brief'],
    use_count: 19,
    content: '# Project brief: storefront-web\nWhat it is: the React storefront (Vite, React 19) that renders product pages, the cart and the checkout UI.\nStack: TypeScript, React Router, TanStack Query.',
  }, 30 * MIN),
  mem(STOREFRONT, {
    key: 'checkout/retry-notice',
    category: 'conventions',
    tags: ['checkout', 'ux'],
    files_affected: ['src/checkout/RetryNotice.tsx'],
    use_count: 4,
    proposed_by: 'cursor',
    content: 'When a capture is being retried, the checkout page shows “Still confirming your payment…” after 1.5 s instead of a spinner, and never offers a second Pay button.',
  }, 5 * DAY),
  mem(STOREFRONT, {
    key: 'cart/reducer-types',
    category: 'conventions',
    tags: ['types'],
    use_count: 3,
    proposed_by: 'cursor',
    content: 'Cart actions are a discriminated union on `type`; the reducer has an exhaustive switch with assertNever in the default branch.',
  }, 7 * DAY),
]

/* ---------- chats ---------- */

type Tool = { name: string; args?: Record<string, unknown>; result?: unknown }
type Turn = { role: 'user' | 'assistant'; text?: string; tools?: Tool[] }
type Chat = { card: ChatCard; turns: Turn[]; summary: string; owner?: string }

const RETRY_DIFF = `--- a/src/payments/retry.ts
+++ b/src/payments/retry.ts
@@ -1,9 +1,24 @@
 import { provider } from './provider'
+import { log } from '../log'

-export async function captureWithRetry(c: Capture) {
-  return provider.capture(c)
+const MAX_ATTEMPTS = Number(process.env.CAPTURE_RETRY_ATTEMPTS ?? 3)
+
+export async function captureWithRetry(c: Capture) {
+  const key = idempotencyKey(c)
+  for (let n = 1; ; n++) {
+    const res = await provider.capture(c, key)
+    if (res.ok) return res
+    if (res.status === 402) throw new Declined(res)
+    if (n >= MAX_ATTEMPTS || !(res.status === 429 || res.status >= 500)) throw new CaptureFailed(c)
+    log.warn('capture retry', { attempt: n, status: res.status })
+    await sleep(backoff(n))
+  }
 }`

const HERO: Turn[] = [
  { role: 'user', text: 'Card captures that get a 503 from the payment provider fail the whole checkout. Retry them with backoff, but make sure we can never charge a card twice.' },
  {
    role: 'assistant',
    tools: [
      { name: 'memory_search', args: { query: 'payment capture retry idempotency' }, result: '2 memories: payments/idempotency-key, conventions/money-minor-units' },
      { name: 'Read', args: { file_path: 'src/payments/retry.ts' }, result: 'export async function captureWithRetry(c: Capture) {\n  return provider.capture(c)\n}' },
      { name: 'Grep', args: { pattern: 'withRetry\\(', path: 'src' }, result: 'src/payments/capture.ts:9:  return withRetry(() => provider.capture(c, key))' },
      {
        name: 'Task',
        args: { description: 'Check provider idempotency-key support', prompt: 'Find out whether our payment provider calls already send an Idempotency-Key, and whether a retried capture with the same key is deduplicated. Report file and line.' },
        result: 'capture.ts creates the key once per capture (line 6) and sends it on line 9. The provider docs in docs/provider.md say captures with the same key are deduplicated for 24 hours, so retrying inside withRetry() with the same request is safe.',
      },
      { name: 'Edit', args: { file_path: 'src/payments/retry.ts', diff: RETRY_DIFF } },
      { name: 'Write', args: { file_path: 'src/payments/retry.test.ts', content: "it('retries 503 then succeeds', ...)\nit('never retries a 402', ...)\nit('stops after CAPTURE_RETRY_ATTEMPTS', ...)" } },
      { name: 'Bash', args: { command: 'npm run test:payments' }, result: ' PASS  src/payments/retry.test.ts (6 tests)\n PASS  src/payments/capture.test.ts (11 tests)\n\nTests: 17 passed, 17 total' },
    ],
  },
  { role: 'assistant', text: 'Captures now retry 429 and 5xx responses with exponential backoff and full jitter: 200 ms base, capped at 2 s, `CAPTURE_RETRY_ATTEMPTS` attempts (default 3).\n\nNo double charges: the sub-agent confirmed `capturePayment()` creates one `Idempotency-Key` per capture and every retry resends it, so the provider deduplicates the same request. A declined card (402) is not retried.\n\nAdded `retry.test.ts`; the payment tests pass.' },
  { role: 'user', text: 'Also log each retry with the attempt number and the provider status.' },
  { role: 'assistant', tools: [{ name: 'Edit', args: { file_path: 'src/payments/retry.ts', old_string: '    await sleep(backoff(n))', new_string: "    log.warn('capture retry', { attempt: n, status: res.status })\n    await sleep(backoff(n))" } }] },
  { role: 'assistant', text: 'Each retry now logs a `warn` line with `attempt` and the provider `status`, and the last attempt rethrows instead of sleeping again. Tests still pass.\n\nI saved this as a project memory (`observability/retry-logging`) so the next agent keeps it.' },
]

const REDACTION: Turn[] = [
  { role: 'user', text: "Here's my .env, set up the sandbox so the capture tests can hit it:\n\nPAYMENTS_API_URL=https://sandbox.payments.example\nPAYMENTS_[REDACTED]\nOPENAI_[REDACTED]\nAWS_ACCESS_KEY_ID=[REDACTED]\n[REDACTED]\nLEDGER_DSN=postgres://localhost:5432/ledger" },
  { role: 'assistant', tools: [{ name: 'Write', args: { file_path: '.env.example', content: 'PAYMENTS_API_URL=\nPAYMENTS_API_KEY=\nLEDGER_DSN=' } }, { name: 'Bash', args: { command: 'git check-ignore .env' }, result: '.env' }] },
  { role: 'assistant', text: '`.env` is already git-ignored, so your keys stay local. I added `.env.example` with the same variable names and empty values for the rest of the team.\n\nSince these keys were pasted into a chat, rotate the sandbox payments key when you get a chance. Nexus stored this conversation with the secrets replaced by `[REDACTED]`.' },
]

function short(user: string, reply: string, tools: Tool[] = []): Turn[] {
  return [{ role: 'user', text: user }, ...(tools.length ? [{ role: 'assistant' as const, tools }] : []), { role: 'assistant', text: reply }]
}

let chatSeq = 0
function chat(project: string, harness: string, title: string, ageMs: number, turns: Turn[], summary: string, owner = ME.id): Chat {
  chatSeq += 1
  const id = `c1a7${String(chatSeq).padStart(4, '0')}-5b8f-4c3e-9a61-2f4d8b7e${String(chatSeq).padStart(4, '0')}`
  return {
    owner,
    summary,
    turns,
    card: { session_id: id, project_id: project, harness, native_id: `native-${chatSeq}`, title, summary, last_active_at: ago(ageMs), turn_count: turns.length },
  }
}

export const CHATS: Chat[] = [
  chat(CHECKOUT, 'claude', 'Retry failed card captures with backoff', 25 * MIN, HERO, 'Added exponential backoff with full jitter to card captures, confirmed idempotency keys make retries safe, and logged each retry.'),
  chat(CHECKOUT, 'cursor', 'Tidy webhook handler types', 1 * DAY + 2 * HOUR, short('Tighten the types in the webhook handler; there are a few `any`s left.', 'Replaced the remaining `any`s with a `WebhookEvent` union keyed on `type`, and the handler switch is now exhaustive. `tsc` is clean.', [{ name: 'Edit', args: { file_path: 'src/webhooks/handler.ts', old_string: 'function handle(evt: any) {', new_string: 'function handle(evt: WebhookEvent) {' } }]), 'Typed the webhook handler with a discriminated union.'),
  chat(CHECKOUT, 'gemini', 'Draft the v2 orders migration notes', 1 * DAY + 5 * HOUR, short('Write migration notes for the v2 orders schema: order, backfill and rollback.', '**Order:** create `order_lines_v2`, backfill in batches of 5,000, then switch reads.\n\n**Rollback:** drop `order_lines_v2` only; v1 tables are untouched until the cleanup release.\n\nI proposed this as a memory for review.'), 'Migration notes for the v2 orders schema.'),
  chat(CHECKOUT, 'claude', 'Review the refund flow for partial captures', 2 * DAY + 3 * HOUR, short('Can a refund on a partially captured payment exceed what we captured?', 'It could. I added a guard in `refunds/partial.ts` that returns 409 `refund_exceeds_capture`, with a test for the boundary case.', [{ name: 'Read', args: { file_path: 'src/refunds/partial.ts' } }, { name: 'Edit', args: { file_path: 'src/refunds/partial.ts', old_string: 'return issueRefund(p, amount)', new_string: "if (amount > p.captured) throw new Conflict('refund_exceeds_capture')\n  return issueRefund(p, amount)" } }]), 'Guarded refunds on partial captures.'),
  chat(CHECKOUT, 'claude', 'Set up local payment sandbox keys', 2 * DAY + 6 * HOUR, REDACTION, 'Set up sandbox keys; secrets were redacted before saving.'),
  chat(CHECKOUT, 'codex', 'Export orders as CSV', 3 * DAY, short('Add a CSV export for orders.', '`GET /orders/export` now streams CSV with a header row and RFC 4180 quoting. Amounts stay in minor units with a currency column.', [{ name: 'Write', args: { file_path: 'src/orders/export.ts', content: 'export async function* ordersCsv(q: OrderQuery) { ... }' } }, { name: 'Bash', args: { command: 'npm test -- orders' }, result: 'Tests: 8 passed, 8 total' }]), 'Streaming CSV export for orders.'),
  chat(CHECKOUT, 'opencode', 'Make capture retry attempts configurable', 4 * DAY, short('Make the number of capture retries configurable.', 'Added `CAPTURE_RETRY_ATTEMPTS` (default 3), read once at start-up and validated as 1-10.'), 'Configurable retry attempts.'),
  chat(CHECKOUT, 'codex', 'Explain the payment state machine', 6 * DAY, short('Explain how a payment moves between states.', '`pending → authorized → captured → (partially_)refunded`. `authorized` can also go to `voided`. Every transition is written to the ledger in the same transaction as the state change.'), 'Walkthrough of the payment state machine.', USERS.priya.id),
  chat(CHECKOUT, 'kimi', 'Bump Go to 1.24 for the ledger worker', 8 * DAY, short('Upgrade the ledger worker to Go 1.24.', 'Bumped `go.mod` to 1.24 with the toolchain pinned, updated the CI image, and `go test ./...` passes.', [{ name: 'Bash', args: { command: 'go test ./...' }, result: 'ok  	acme/ledger	2.418s' }]), 'Go 1.24 for the ledger worker.'),
  chat(STOREFRONT, 'cursor', 'Show a retry notice on the checkout page', 3 * DAY + 4 * HOUR, short('While a capture is retrying, show the customer something better than a spinner.', 'After 1.5 s the button area shows “Still confirming your payment…”, and the Pay button stays disabled so nobody pays twice.'), 'Retry notice on checkout.'),
  chat(STOREFRONT, 'cursor', 'Tidy cart reducer types', 5 * DAY, short('The cart reducer accepts any action. Fix the types.', 'Cart actions are now a discriminated union; the reducer switch is exhaustive with `assertNever`.'), 'Typed cart reducer.'),
  chat(STOREFRONT, 'claude', 'Lazy-load the product gallery', 9 * DAY, short('The product page ships the whole gallery up front. Can we lazy-load it?', 'The gallery is now a separate chunk loaded when it scrolls into view; the product page bundle is 38 kB smaller.'), 'Lazy-loaded the gallery.', USERS.daniel.id),
]

export function chatTurns(c: Chat): ChatTurn[] {
  const start = NOW - Date.parse(c.card.last_active_at) + c.turns.length * 2 * MIN
  return c.turns.map((t, idx) => ({
    idx,
    role: t.role,
    text_preview: t.text ?? '',
    tool_calls: t.tools,
    source_ts: ago(start - idx * 2 * MIN),
  }))
}

export function chatSession(c: Chat): ChatSession {
  return {
    id: c.card.session_id,
    project_id: c.card.project_id,
    harness: c.card.harness,
    native_id: c.card.native_id,
    title: c.card.title,
    summary: c.summary,
    owner_user_id: c.owner,
    workspace_root_hint: `D:\\code\\${PROJECTS.find((p) => p.id === c.card.project_id)?.folder_name ?? ''}`,
    last_active_at: c.card.last_active_at,
  }
}

/* ---------- activity ---------- */

let evSeq = 1000
function ev(project: string, event_type: string, ageMs: number, payload: Record<string, unknown>, user = ME.id, agent?: string): ProjectEvent {
  evSeq += 1
  return { id: evSeq, project_id: project, event_type, user_id: user, agent_id: agent, payload, created_at: ago(ageMs) }
}

export const EVENTS: ProjectEvent[] = [
  ev(CHECKOUT, 'MCP_TOOL_CALL', 26 * MIN, { tool_name: 'memory_search', agent_id: 'claude' }, ME.id, 'claude'),
  ev(CHECKOUT, 'MEMORY_PROPOSED', 85 * MIN, { key: 'observability/retry-logging' }, ME.id, 'claude'),
  ev(CHECKOUT, 'MEMORY_CONFIRMED', 80 * MIN, { key: 'observability/retry-logging' }),
  ev(CHECKOUT, 'MEMORY_UPDATED', 2 * HOUR, { key: 'payments/capture-retries' }),
  ev(CHECKOUT, 'MCP_TOOL_CALL', 3 * HOUR, { tool_name: 'why', agent_id: 'codex' }, USERS.priya.id, 'codex'),
  ev(CHECKOUT, 'SESSION_HANDOFF_INITIATED', 5 * HOUR, { note: 'Retry work is ready for review' }, ME.id),
  ev(CHECKOUT, 'SESSION_HANDOFF_ACCEPTED', 4 * HOUR, {}, USERS.priya.id),
  ev(CHECKOUT, 'MEMORY_PROPOSED', 22 * HOUR, { key: 'orders/v2-migration' }, ME.id, 'gemini'),
  ev(CHECKOUT, 'MCP_TOOL_CALL', 1 * DAY, { tool_name: 'project_knowledge', agent_id: 'cursor' }, ME.id, 'cursor'),
  ev(CHECKOUT, 'MEMORY_CONFIRMED', 2 * DAY, { key: 'refunds/partial-captures' }, USERS.priya.id),
  ev(CHECKOUT, 'MCP_TOOL_CALL', 2 * DAY, { tool_name: 'related_to', agent_id: 'claude' }, ME.id, 'claude'),
  ev(CHECKOUT, 'MEMORY_CONFIRMED', 3 * DAY, { key: 'infra/ledger-worker-go' }, USERS.daniel.id),
  ev(CHECKOUT, 'MEMORY_PROPOSED', 5 * DAY, { key: 'exports/orders-csv' }, ME.id, 'codex'),
  ev(CHECKOUT, 'MEMORY_REJECTED', 6 * DAY, { key: 'payments/retry-on-402' }, USERS.priya.id),
  ev(STOREFRONT, 'MEMORY_CONFIRMED', 5 * DAY, { key: 'checkout/retry-notice' }, USERS.priya.id),
  ev(STOREFRONT, 'MCP_TOOL_CALL', 5 * DAY, { tool_name: 'file_history', agent_id: 'cursor' }, ME.id, 'cursor'),
  ev(STOREFRONT, 'MEMORY_PROPOSED', 7 * DAY, { key: 'cart/reducer-types' }, ME.id, 'cursor'),
]

/** ~52 weeks of activity with a believable weekly rhythm and a busy recent stretch. */
function heatmap(seed: number) {
  const out: { date: string; count: number }[] = []
  for (let d = 364; d >= 0; d--) {
    const day = new Date(NOW - d * DAY)
    const weekday = day.getUTCDay()
    const wave = Math.sin((d + seed) * 0.37) + Math.sin((d + seed) * 0.11)
    let count = Math.max(0, Math.round((weekday === 0 || weekday === 6 ? 0.6 : 3.2) + wave * 2.4))
    if (d > 300) count = Math.round(count * 0.3)
    if (d < 21) count += 3
    out.push({ date: day.toISOString().slice(0, 10), count })
  }
  return out
}

export function dashboard(project: string): ProjectDashboard {
  const memories = MEMORIES.filter((m) => m.project_id === project && m.status !== 'REJECTED')
  const recent = EVENTS.filter((e) => e.project_id === project)
  const byType = new Map<string, number>()
  for (const e of recent) byType.set(e.event_type, (byType.get(e.event_type) ?? 0) + 1)
  const checkout = project === CHECKOUT
  return {
    project_id: project,
    metrics: {
      memories: memories.length,
      proposed: memories.filter((m) => m.status === 'PROPOSED').length,
      confirmed: memories.filter((m) => m.status === 'CONFIRMED').length,
      members: 3,
      online: checkout ? 2 : 1,
      events_7d: checkout ? 46 : 12,
      agent_calls: checkout ? 312 : 74,
      github_connected: true,
      github_repo: checkout ? 'acme/checkout-service' : 'acme/storefront-web',
    },
    heatmap: heatmap(checkout ? 3 : 11),
    by_type: [...byType].map(([type, count]) => ({ type, count })),
    recent,
  }
}

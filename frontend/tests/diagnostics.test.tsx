import assert from 'node:assert/strict'
import test from 'node:test'
import {
  MAX_AUTO_REPORTS_PER_SESSION,
  breadcrumb,
  buildErrorReport,
  buildUserReport,
  disableDiagnostics,
  flush,
  osLabel,
  queuedReports,
  recentLogs,
  redact,
  refForToast,
  rememberErrorRef,
  reportError,
  resetDiagnosticsForTests,
  setErrorReportsEnabled,
  stripUrlQueries,
} from '../src/lib/diagnostics'

type Call = { url: string; init: RequestInit }

function fakeFetch(): Call[] {
  const calls: Call[] = []
  globalThis.fetch = (async (url: string, init: RequestInit) => {
    calls.push({ url, init })
    return new Response(JSON.stringify({ report_id: 'r', fingerprint: 'f', duplicate: false, request_id: 'REQ' }), { status: 202 })
  }) as typeof fetch
  return calls
}

// localStorage stand-in so the consent switch can be exercised.
const store = new Map<string, string>()
;(globalThis as { localStorage?: unknown }).localStorage = {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => void store.set(k, v),
  removeItem: (k: string) => void store.delete(k),
}

test('redaction removes tokens, emails, home paths and query strings', () => {
  const jwt = 'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJlLXZhbHVl'
  const input = [
    'Authorization: Bearer abcdef1234567890',
    `jwt ${jwt}`,
    'nxs_AbC123xyz_-987 and sk-ant-api03-zzzzzzzzzzzzzzzz',
    'mail alice@example.com',
    'C:\\Users\\Pratyush Mishra\\AppData\\x.log',
    '/Users/alice/src/app.ts',
    'GET https://api.example/v1/memory/search?q=private+plans&token=abc#frag',
    'GET /memory/search?q=private',
    '{"password": "hunter22"}',
  ].join('\n')
  const out = redact(input)
  for (const leak of ['abcdef1234567890', 'eyJzdWIi', 'AbC123xyz', 'zzzzzzzz', 'alice@', 'Pratyush', 'Mishra', '/Users/alice', 'private', 'token=abc', 'frag', 'hunter22']) {
    assert.ok(!out.includes(leak), `leaked ${leak}: ${out}`)
  }
  assert.match(out, /https:\/\/api\.example\/v1\/memory\/search(\s|$)/)
  assert.equal(stripUrlQueries('see https://x.dev/a/b?c=d.'), 'see https://x.dev/a/b')
})

test('error reports carry no chat content and only the path of the page', () => {
  resetDiagnosticsForTests()
  const err = new SyntaxError('Unexpected token \'H\', "Here is my private chat message about the merger" is not valid JSON')
  const r = buildErrorReport(err, 'error')
  assert.equal(r.source, 'web')
  assert.ok(!r.message.includes('merger'), r.message)
  assert.match(r.message, /^SyntaxError: /)
  assert.ok(r.report_id.length >= 8)
})

test('breadcrumbs are capped and redacted', () => {
  resetDiagnosticsForTests()
  for (let i = 0; i < 60; i++) breadcrumb(`GET /v1/things/${i}?secret=s${i} -> 200`)
  const logs = recentLogs()
  assert.equal(logs.length, 40)
  assert.ok(logs.every((l) => !l.includes('secret=')))
  assert.match(logs[39], /things\/59 -> 200$/)
})

test('user report preview is what is sent: note redacted, logs optional', () => {
  resetDiagnosticsForTests()
  breadcrumb('GET /projects -> 500 ref=ABC')
  const withLogs = buildUserReport('Sync is stuck\nmy email is bob@example.com', { includeLogs: true })
  assert.equal(withLogs.kind, 'user_report')
  assert.equal(withLogs.message, 'Sync is stuck')
  assert.ok(!withLogs.user_note!.includes('bob@'))
  assert.equal(withLogs.logs!.length, 1)
  const without = buildUserReport('x y z', { includeLogs: false })
  assert.equal(without.logs, undefined)
})

test('automatic reports: deduped, capped per session, sent anonymously', async () => {
  resetDiagnosticsForTests()
  const calls = fakeFetch()
  // The same error from the same place (a render loop) is one report.
  const boom = () => new Error('boom')
  assert.equal(reportError(boom(), 'crash'), true)
  assert.equal(reportError(boom(), 'crash'), false, 'duplicate in the same session')
  for (let i = 0; i < 10; i++) reportError(new Error(`distinct ${i}`), 'error')
  assert.equal(queuedReports().length, MAX_AUTO_REPORTS_PER_SESSION)
  await flush(false)
  assert.equal(calls.length, MAX_AUTO_REPORTS_PER_SESSION)
  for (const c of calls) {
    assert.match(c.url, /\/v1\/diagnostics$/)
    assert.equal((c.init.headers as Record<string, string>).Authorization, undefined)
    const body = JSON.parse(String(c.init.body))
    assert.equal(body.source, 'web')
  }
  assert.equal(reportError(new Error('one more'), 'error'), false, 'session cap reached')
})

test('nothing is sent when reports are off, in the demo, or for noise', () => {
  resetDiagnosticsForTests()
  setErrorReportsEnabled(false)
  assert.equal(reportError(new Error('off'), 'crash'), false)
  setErrorReportsEnabled(true)

  const apiError = Object.assign(new Error('forbidden'), { name: 'ApiError', status: 403 })
  assert.equal(reportError(apiError, 'error'), false)
  assert.equal(reportError(new Error('ResizeObserver loop limit exceeded'), 'error'), false)
  const ext = new Error('x')
  ext.stack = 'Error: x\n    at chrome-extension://abc/content.js:1:1'
  assert.equal(reportError(ext, 'error'), false)

  disableDiagnostics()
  assert.equal(reportError(new Error('demo'), 'crash'), false)
  resetDiagnosticsForTests()
})

test('error toasts find the request ID of the failure they describe', () => {
  resetDiagnosticsForTests()
  const now = Date.now()
  rememberErrorRef('could not list members', 'REF111111111')
  rememberErrorRef('token expired', 'REF222222222')
  assert.equal(refForToast('could not list members', now), 'REF111111111')
  assert.equal(refForToast('something unrelated', now), 'REF222222222', 'falls back to the latest failure within 3 s')
  assert.equal(refForToast('something unrelated', now + 60_000), undefined)
})

test('OS label is coarse', () => {
  assert.equal(osLabel('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36'), 'Windows · Chrome 131')
  assert.equal(osLabel('Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 Version/17.5 Safari/605.1.15'), 'macOS · Safari 17')
})

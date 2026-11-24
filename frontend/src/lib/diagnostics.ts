/**
 * Problem reports from the web app (POST /v1/diagnostics).
 *
 * Two paths:
 * - Automatic error reports: screen crashes (AppErrorBoundary), uncaught
 *   errors and unhandled promise rejections. On by default, can be turned
 *   off in Settings, sent without the account token (anonymous), batched,
 *   deduplicated and capped per tab session.
 * - "Report a problem": the user writes a note, sees exactly what will be
 *   sent, and sends it linked to their account.
 *
 * What is never sent: chat or memory content, page text, request or
 * response bodies, tokens, query strings or URL fragments. Everything that
 * is sent passes through redact() first; the server redacts again.
 */
import { readPref, writePref } from '@/utils/prefs'

export type ReportKind = 'crash' | 'error' | 'user_report'

/** Wire format, shared with the desktop app and Go core (see the server PR). */
export type DiagnosticReport = {
  report_id: string
  kind: ReportKind
  source: 'web'
  app_version: string
  os: string
  message: string
  stack?: string
  logs?: string[]
  user_note?: string
  timestamp: string
  context?: Record<string, string>
}

export type SendResult = { report_id: string; fingerprint: string; duplicate: boolean; request_id: string }

const PREF_KEY = 'nexus.errorReports'
const SESSION_COUNT_KEY = 'nexus.errorReports.sent'
const SESSION_SEEN_KEY = 'nexus.errorReports.seen'
/** Automatic reports per tab session; the rest are dropped. */
export const MAX_AUTO_REPORTS_PER_SESSION = 5
const FLUSH_DELAY_MS = 2500
const MAX_LOG_LINES = 40
const MAX_MESSAGE = 500
const MAX_STACK = 12_000
const MAX_NOTE = 4000

// --- consent ---------------------------------------------------------------

/** Automatic error reports are on unless the user turned them off here. */
export function errorReportsEnabled(): boolean {
  return readPref(PREF_KEY) !== 'off'
}

export function setErrorReportsEnabled(on: boolean) {
  writePref(PREF_KEY, on ? 'on' : 'off')
}

let disabled = false

/** The /demo app and tests call this: nothing is ever sent. */
export function disableDiagnostics() {
  disabled = true
}

export function isDemo(): boolean {
  if (typeof window === 'undefined') return false
  const path = window.location.pathname
  return path === '/demo' || path.startsWith('/demo/') || document.documentElement.classList.contains('nx-demo')
}

function sendingAllowed(): boolean {
  return !disabled && !isDemo()
}

// --- redaction ---------------------------------------------------------------

const RULES: [RegExp, string][] = [
  [/\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}/gi, '$1 [redacted]'],
  [/\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}/g, '[redacted]'],
  [/\bnxs_[A-Za-z0-9_-]{6,}/g, 'nxs_[redacted]'],
  [/\bsk-[A-Za-z0-9_-]{12,}/g, 'sk-[redacted]'],
  [/\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})/g, '[redacted]'],
  [/\b(AKIA|ASIA)[0-9A-Z]{16}\b/g, '[redacted]'],
  [/([a-zA-Z][a-zA-Z0-9+.-]*:\/\/)[^/\s:@]+:[^/\s@]+@/g, '$1[redacted]@'],
  [
    /("?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|auth[_-]?token|client[_-]?secret|secret|password|passwd|authorization|cookie|token)"?\s*[:=]\s*"?)[^\s"',;&]{3,}/gi,
    '$1[redacted]',
  ],
  [/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g, '[email]'],
  [/\b[A-Z]:\\\\(?:Users|Documents and Settings)\\\\[^\\"'<>|\r\n]+/gi, '~'],
  [/\b[A-Z]:[\\/](?:Users|Documents and Settings)[\\/][^\\/"'<>|:\r\n]+/gi, '~'],
  [/(^|[^A-Za-z0-9._~-])\/(?:Users|home)\/[^/\s"'<>:]+/g, '$1~'],
]

/** Query strings and fragments carry tokens, codes and search text: drop them. */
export function stripUrlQueries(s: string): string {
  return s
    .replace(/(\b[a-z][a-z0-9+.-]*:\/\/[^\s?#"'()<>]*)[?#][^\s"'()<>]*/gi, '$1')
    .replace(/((?:^|[\s"'(])\/[A-Za-z0-9_\-./~%]*)\?[^\s"'()<>]*/g, '$1')
}

/** Long quoted strings in error messages are usually user content (JSON parse errors quote it). */
function dropQuotedContent(s: string): string {
  return s.replace(/"[^"\n]{40,}"|'[^'\n]{40,}'/g, '"[text]"')
}

export function redact(s: string): string {
  let out = stripUrlQueries(s)
  for (const [re, repl] of RULES) out = out.replace(re, repl)
  return out
}

function cap(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n)}…` : s
}

// --- breadcrumbs ---------------------------------------------------------------

const logs: string[] = []

/** A content-free log line (API call, navigation). Kept in memory, last 40. */
export function breadcrumb(line: string) {
  const stamp = new Date().toISOString().slice(11, 23)
  logs.push(`${stamp} ${cap(redact(line), 300)}`)
  if (logs.length > MAX_LOG_LINES) logs.splice(0, logs.length - MAX_LOG_LINES)
}

export function recentLogs(): string[] {
  return [...logs]
}

// --- request references for toasts ---------------------------------------------

type Ref = { message: string; id: string; at: number }
const refs: Ref[] = []

/** Remember the request ID behind a failed API call so its toast can show it. */
export function rememberErrorRef(message: string, id: string) {
  if (!id) return
  refs.push({ message, id, at: Date.now() })
  if (refs.length > 20) refs.shift()
}

/** The request ID for an error toast: the failure with this text, else one from the last 3 s. */
export function refForToast(text: string | undefined, now = Date.now()): string | undefined {
  for (let i = refs.length - 1; i >= 0; i--) {
    const r = refs[i]
    if (now - r.at > 15_000) break
    if (text && (text === r.message || text.includes(r.message))) return r.id
  }
  const last = refs[refs.length - 1]
  return last && now - last.at <= 3000 ? last.id : undefined
}

export function lastErrorRef(): string | undefined {
  const last = refs[refs.length - 1]
  return last && Date.now() - last.at <= 10 * 60_000 ? last.id : undefined
}

// --- environment ---------------------------------------------------------------

export function appVersion(): string {
  return (import.meta.env?.VITE_APP_VERSION as string | undefined) ?? 'dev'
}

/** Coarse platform and browser, e.g. "Windows · Chrome 131". No full user agent. */
export function osLabel(ua = typeof navigator === 'undefined' ? '' : navigator.userAgent): string {
  const os = /Windows/.test(ua)
    ? 'Windows'
    : /iPhone|iPad|iPod/.test(ua)
      ? 'iOS'
      : /Mac OS X|Macintosh/.test(ua)
        ? 'macOS'
        : /Android/.test(ua)
          ? 'Android'
          : /Linux/.test(ua)
            ? 'Linux'
            : 'Unknown OS'
  const m = /(Edg|OPR|Firefox|Chrome|Version)\/(\d+)/.exec(ua)
  const names: Record<string, string> = { Edg: 'Edge', OPR: 'Opera', Firefox: 'Firefox', Chrome: 'Chrome', Version: 'Safari' }
  return m ? `${os} · ${names[m[1]]} ${m[2]}` : os
}

/** Same as API_BASE in lib/env, read lazily so this module loads outside Vite (tests). */
function apiBase(): string {
  return (import.meta.env?.VITE_API_BASE as string | undefined) ?? '/api'
}

function route(): string {
  return typeof window === 'undefined' ? '' : window.location.pathname
}

function newReportId(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID()
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
}

// --- building reports ---------------------------------------------------------------

function errorParts(error: unknown): { message: string; stack: string } {
  if (error instanceof Error) {
    const name = error.name && error.name !== 'Error' ? `${error.name}: ` : ''
    return { message: `${name}${error.message || 'Unknown error'}`, stack: error.stack ?? '' }
  }
  if (typeof error === 'string') return { message: error, stack: '' }
  return { message: 'Non-error value thrown', stack: '' }
}

export function buildErrorReport(error: unknown, kind: 'crash' | 'error', extra?: { componentStack?: string; via?: string }): DiagnosticReport {
  const { message, stack } = errorParts(error)
  const fullStack = [stack, extra?.componentStack ? `Component stack:${extra.componentStack}` : ''].filter(Boolean).join('\n')
  const context: Record<string, string> = { route: route() }
  if (extra?.via) context.via = extra.via
  const ref = lastErrorRef()
  if (ref) context.last_request_id = ref
  return {
    report_id: newReportId(),
    kind,
    source: 'web',
    app_version: appVersion(),
    os: osLabel(),
    message: cap(dropQuotedContent(redact(message)), MAX_MESSAGE),
    stack: cap(redact(fullStack), MAX_STACK) || undefined,
    logs: recentLogs(),
    timestamp: new Date().toISOString(),
    context,
  }
}

/** The report "Report a problem" will send, shown to the user before sending. */
export function buildUserReport(note: string, opts: { includeLogs: boolean }): DiagnosticReport {
  const trimmed = cap(redact(note.trim()), MAX_NOTE)
  const context: Record<string, string> = { route: route() }
  const ref = lastErrorRef()
  if (ref) context.last_request_id = ref
  return {
    report_id: newReportId(),
    kind: 'user_report',
    source: 'web',
    app_version: appVersion(),
    os: osLabel(),
    message: cap(trimmed.split('\n')[0] || 'Problem report', 200),
    user_note: trimmed,
    logs: opts.includeLogs ? recentLogs() : undefined,
    timestamp: new Date().toISOString(),
    context,
  }
}

// --- sending ---------------------------------------------------------------

/** POST one report. The account token is attached only when linkAccount is set. */
export async function sendReport(report: DiagnosticReport, opts: { token?: string | null; keepalive?: boolean } = {}): Promise<SendResult> {
  if (!sendingAllowed()) throw new Error('Problem reports are not sent from the demo.')
  const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json' }
  if (opts.token) headers.Authorization = `Bearer ${opts.token}`
  const res = await fetch(`${apiBase()}/v1/diagnostics`, {
    method: 'POST',
    headers,
    body: JSON.stringify(report),
    keepalive: opts.keepalive,
  })
  const text = await res.text()
  let data: unknown = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    /* not JSON */
  }
  if (!res.ok) {
    const body = data as { error?: { message?: string; request_id?: string } } | null
    const id = body?.error?.request_id ?? res.headers.get('X-Request-ID') ?? ''
    const msg = body?.error?.message || res.statusText || 'Could not send the report'
    throw new Error(id ? `${msg} (ref ${id})` : msg)
  }
  return data as SendResult
}

// --- automatic queue ---------------------------------------------------------------

const queue: DiagnosticReport[] = []
let timer: ReturnType<typeof setTimeout> | undefined

function sessionNumber(key: string): number {
  try {
    return Number(sessionStorage.getItem(key) ?? '0') || 0
  } catch {
    return memoryCount
  }
}
let memoryCount = 0
const memorySeen = new Set<string>()

function seenBefore(sig: string): boolean {
  if (memorySeen.has(sig)) return true
  memorySeen.add(sig)
  try {
    const list = JSON.parse(sessionStorage.getItem(SESSION_SEEN_KEY) ?? '[]') as string[]
    if (list.includes(sig)) return true
    list.push(sig)
    sessionStorage.setItem(SESSION_SEEN_KEY, JSON.stringify(list.slice(-50)))
  } catch {
    /* storage blocked: the in-memory set still dedupes */
  }
  return false
}

function countSent(): number {
  memoryCount += 1
  try {
    const n = sessionNumber(SESSION_COUNT_KEY) + 1
    sessionStorage.setItem(SESSION_COUNT_KEY, String(n))
    return n
  } catch {
    return memoryCount
  }
}

/** Same problem = same kind, message and first stack line. */
export function signature(r: DiagnosticReport): string {
  const frame = (r.stack ?? '').split('\n').find((l) => /^\s*at\s|@/.test(l))?.trim() ?? ''
  return `${r.kind}|${r.message}|${frame}`
}

function ignorable(error: unknown): boolean {
  const { message, stack } = errorParts(error)
  if (/chrome-extension:|moz-extension:|safari-(web-)?extension:/.test(stack)) return true
  if (/^Script error\.?$/.test(message)) return true // cross-origin, no details
  if (/ResizeObserver loop/.test(message)) return true
  if (error instanceof Error && error.name === 'AbortError') return true
  // API failures are already logged by the server with their request ID.
  const status = (error as { status?: unknown } | null)?.status
  if (error instanceof Error && error.name === 'ApiError' && typeof status === 'number' && status < 500) return true
  return false
}

/** Queue an automatic report. Returns false when it was dropped. */
export function reportError(error: unknown, kind: 'crash' | 'error', extra?: { componentStack?: string; via?: string }): boolean {
  if (!sendingAllowed() || !errorReportsEnabled() || ignorable(error)) return false
  const report = buildErrorReport(error, kind, extra)
  if (seenBefore(signature(report))) return false
  if (sessionNumber(SESSION_COUNT_KEY) + queue.length >= MAX_AUTO_REPORTS_PER_SESSION) return false
  queue.push(report)
  if (timer === undefined) timer = setTimeout(() => void flush(false), FLUSH_DELAY_MS)
  return true
}

/** Send what is queued. On page hide the browser keeps the requests alive. */
export async function flush(keepalive: boolean) {
  if (timer !== undefined) {
    clearTimeout(timer)
    timer = undefined
  }
  const batch = queue.splice(0, queue.length)
  for (const report of batch) {
    if (countSent() > MAX_AUTO_REPORTS_PER_SESSION) return
    try {
      // Anonymous on purpose: automatic reports are not linked to the account.
      await sendReport(report, { keepalive })
    } catch {
      /* best effort: a failed report is not retried */
    }
  }
}

let installed = false

/** Catch uncaught errors and unhandled rejections anywhere in the app. */
export function installGlobalErrorHandlers() {
  if (installed || typeof window === 'undefined') return
  installed = true
  window.addEventListener('error', (e) => {
    // Resource load failures (img/script) arrive here without an error object.
    if (!e.error && !e.message) return
    reportError(e.error ?? e.message, 'crash', { via: 'window.onerror' })
  })
  window.addEventListener('unhandledrejection', (e) => {
    reportError(e.reason, 'error', { via: 'unhandledrejection' })
  })
  window.addEventListener('pagehide', () => {
    if (queue.length) void flush(true)
  })
}

/** Test hook: clear queue, counters and dedupe state. */
export function resetDiagnosticsForTests() {
  queue.splice(0, queue.length)
  logs.splice(0, logs.length)
  refs.splice(0, refs.length)
  memorySeen.clear()
  memoryCount = 0
  disabled = false
  if (timer !== undefined) clearTimeout(timer)
  timer = undefined
  try {
    sessionStorage.removeItem(SESSION_COUNT_KEY)
    sessionStorage.removeItem(SESSION_SEEN_KEY)
  } catch {
    /* no storage in tests */
  }
}

export function queuedReports(): DiagnosticReport[] {
  return [...queue]
}

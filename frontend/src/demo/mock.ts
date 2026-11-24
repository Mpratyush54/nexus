/*
 * Demo mode plumbing, installed before the app renders and only on /demo:
 * - localStorage is swapped for an in-memory copy, so the demo's sample
 *   session never touches (or reads) a real signed-in session in this browser;
 * - fetch answers the API from ./api and refuses every other origin, so the
 *   demo cannot reach the Nexus server, the local desktop bridge or anything else;
 * - WebSocket is a stand-in that opens and stays quiet.
 */
import { API_BASE } from '@/lib/env'
import { handle } from './api'
import { CHECKOUT, ME } from './data'

export const DEMO_TOKEN = 'demo-session'

class MemoryStorage implements Storage {
  private map = new Map<string, string>()
  get length() { return this.map.size }
  clear() { this.map.clear() }
  getItem(key: string) { return this.map.has(key) ? this.map.get(key)! : null }
  key(i: number) { return [...this.map.keys()][i] ?? null }
  removeItem(key: string) { this.map.delete(key) }
  setItem(key: string, value: string) { this.map.set(key, String(value)) }
}

function installStorage() {
  let theme: string | null = null
  try { theme = window.localStorage.getItem('nexus.theme') } catch { /* blocked storage */ }
  const mem = new MemoryStorage()
  if (theme) mem.setItem('nexus.theme', theme)
  mem.setItem('nexus.auth.token', DEMO_TOKEN)
  mem.setItem('nexus.auth.user', JSON.stringify({ userId: ME.id, username: ME.username }))
  mem.setItem('nexus.project.id', CHECKOUT)
  mem.setItem('nexus.onboarding.done', '1')
  Object.defineProperty(window, 'localStorage', { value: mem, configurable: true })
  Object.defineProperty(window, 'sessionStorage', { value: new MemoryStorage(), configurable: true })
}

function json(status: number, body: unknown) {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function installFetch() {
  const realFetch = window.fetch.bind(window)
  const apiBase = new URL(API_BASE, location.origin)
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : null
    const url = new URL(req ? req.url : String(input), location.origin)
    const method = (init?.method ?? req?.method ?? 'GET').toUpperCase()
    const apiPath = url.href.startsWith(apiBase.href.replace(/\/$/, '') + '/') ? url.href.slice(apiBase.href.replace(/\/$/, '').length) : null
    if (apiPath !== null) {
      const path = new URL(apiPath, 'http://demo.invalid')
      let body: unknown
      const raw = init?.body ?? null
      if (typeof raw === 'string') { try { body = JSON.parse(raw) } catch { body = raw } }
      // A short pause so loading states look like the real app, not a flash.
      await new Promise((r) => setTimeout(r, 120 + Math.random() * 180))
      if (init?.signal?.aborted) throw new DOMException('Aborted', 'AbortError')
      const res = handle({ method, path: path.pathname, query: path.searchParams, body })
      return json(res.status, res.body)
    }
    // Same-origin static files (route chunks, fonts, images) load normally.
    if (url.origin === location.origin && !url.pathname.startsWith('/ws')) return realFetch(input, init)
    // Anything else (the hosted API, the desktop bridge on 127.0.0.1, third parties) is unreachable here.
    throw new TypeError('Network access is disabled in the Nexus demo')
  }
}

class QuietSocket extends EventTarget {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSING = 2
  readonly CLOSED = 3
  readyState = 0
  binaryType: BinaryType = 'blob'
  bufferedAmount = 0
  extensions = ''
  protocol = ''
  url: string
  onopen: ((ev: Event) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  onclose: ((ev: CloseEvent) => void) | null = null
  constructor(url: string | URL) {
    super()
    this.url = String(url)
    setTimeout(() => {
      if (this.readyState !== 0) return
      this.readyState = 1
      const ev = new Event('open')
      this.onopen?.(ev)
      this.dispatchEvent(ev)
    }, 50)
  }
  send() { /* the demo has no server to talk to */ }
  close() {
    if (this.readyState >= 2) return
    this.readyState = 3
    const ev = new CloseEvent('close', { code: 1000, wasClean: true })
    this.onclose?.(ev)
    this.dispatchEvent(ev)
  }
}

export function installDemoMocks() {
  installStorage()
  installFetch()
  Object.defineProperty(window, 'WebSocket', { value: QuietSocket, configurable: true, writable: true })
  // Never let a demo page register (or be served by) the app's service worker.
  if ('serviceWorker' in navigator) {
    Object.defineProperty(navigator.serviceWorker, 'register', { value: () => Promise.reject(new Error('disabled in demo')), configurable: true })
  }
}

/*
 * The living background of the hero: Nexus at work, played out in faint,
 * ghosted pieces of the app around the headline.
 *
 * Each 16 s cycle tells one story, clockwise round the headline:
 *   1. top left: a chat with one agent ("cap retries at 3, backoff 2s");
 *      Nexus captures the decision (or redacts a secret first),
 *   2. the decision travels over the top into the memory card (top right),
 *   3. memory flows down to a new session with another agent (bottom right):
 *      "context loaded · from Claude Code", and its reply already uses it,
 *   4. the code it writes travels under the buttons to a file card (bottom
 *      left) whose line carries its "why", traced back up to the first chat;
 *      one story instead shows the change being undone.
 * The source and target agent rotate each cycle, so all six take part, and
 * the memory card keeps what earlier cycles saved.
 *
 * Where the sides are too narrow (phones, tablets) one small strip plays
 * the same story under the buttons: agent → Nexus → agent, with a caption.
 *
 * Canvas 2D inside the hero (so it scrolls with the text and never overlaps
 * it: every piece is placed from the measured text boxes), about 30 fps,
 * loaded after first paint by client.ts. It pauses while the tab is hidden,
 * the hero is off screen or the film player is open; with reduced motion it
 * draws one still frame of the whole story. Decoration only: aria-hidden,
 * no pointer events.
 */

type RGB = readonly [number, number, number]
type Box = { x: number; y: number; w: number; h: number; s: number }
type Path = { xs: number[]; ys: number[]; ls: number[]; total: number }

const INK: RGB = [236, 235, 230]
const INK2: RGB = [169, 172, 167]
const INK3: RGB = [141, 145, 139]
const AMBER: RGB = [232, 187, 103]
const RULE: RGB = [52, 56, 53]
const PANEL: RGB = [19, 21, 20]

// The agent dots from DESIGN.md, in the pipeline's order.
export const AGENTS: { name: string; rgb: RGB }[] = [
  { name: 'Claude Code', rgb: [217, 137, 91] },
  { name: 'Codex', rgb: [154, 167, 240] },
  { name: 'Cursor', rgb: [191, 197, 194] },
  { name: 'OpenCode', rgb: [127, 194, 166] },
  { name: 'Gemini', rgb: [122, 168, 240] },
  { name: 'Kimi', rgb: [197, 154, 230] },
]

export type Story = {
  a: number // the agent the decision comes from
  b: number // the agent that picks it up
  say: string
  secret?: [number, number] // a key inside `say`, redacted when captured
  ok: string
  decision: string
  ask: string
  reply: string
  file: string
  line: number
  code: [string, string]
  undo?: boolean
}

// Every source and every target is a different agent, so all six appear in both roles.
export const STORIES: Story[] = [
  { a: 0, b: 1, say: 'cap retries at 3, backoff 2s', ok: 'Got it. Capping at 3.', decision: 'retries capped at 3', ask: 'add retries to the uploader', reply: 'Using it: 3 tries, 2s backoff', file: 'retry.ts', line: 15, code: ['const maxRetries = 3', 'const backoffMs = 2000'] },
  { a: 2, b: 4, say: 'use pgx, not database/sql', ok: 'Switching to pgx.', decision: 'Postgres via pgx', ask: 'write the user store', reply: 'Using pgx, as decided.', file: 'store.go', line: 22, code: ['pool, err := pgxpool.New(', '  ctx, cfg.DatabaseURL)'] },
  { a: 3, b: 5, say: 'key is sk_live_51HqX9, use .env', secret: [7, 21], ok: 'Moving it to .env.', decision: 'keys live in .env only', ask: 'wire up payments', reply: 'Reading the key from .env.', file: 'pay.ts', line: 8, code: ['const key = env.STRIPE_KEY', 'if (!key) throw noKey()'] },
  { a: 1, b: 2, say: 'store all dates in UTC', ok: 'Done. UTC everywhere.', decision: 'dates stored in UTC', ask: 'add the CSV export', reply: 'Exporting dates as UTC.', file: 'export.ts', line: 31, code: ['at: row.at.toISOString(),', "tz: 'UTC',"] },
  { a: 4, b: 0, say: 'sessions expire after 15 min', ok: 'TTL set to 15 minutes.', decision: 'sessions last 15 min', ask: 'add token refresh', reply: 'Refreshing before 15 min.', file: 'auth.go', line: 88, code: ['ttl := 15 * time.Minute', 'exp := now.Add(ttl)'] },
  { a: 5, b: 3, say: 'named exports only, please', ok: 'Will do.', decision: 'named exports only', ask: 'split the API client', reply: 'Splitting, named exports.', file: 'client.ts', line: 12, code: ['export function request(', 'export function upload('], undo: true },
]
const SEED: [string, string, number][] = [['Brief', 'Go API + React web', 3], ['Decision', 'tests before merge', 4], ['Decision', 'no ORMs', 1]]

export const CYCLE = 16
/** The moment a still frame shows: every card on screen, context loaded, the line's why. */
export const STILL_AT = 14.7

// Beats of a cycle, in seconds.
const P = {
  type: [0.5, 2.3], ok: 2.6, cap: 3.1, up: [3.6, 5.6], mem: 5.6, down: [6.0, 7.0], bIn: 6.6, ctx: 7.0,
  ask: [7.6, 9.2], reply: 9.6, under: [10.2, 12.0], cIn: 11.6, code: [12.0, 12.8], why: 12.9, back: [13.4, 14.4], out: 15.0,
} as const

const CW = 264 // card width, local units
const SH = 112 // session card height
const MH = 108 // memory card height
const CH = 140 // code card height
const MONO = '400 10.5px "IBM Plex Mono", ui-monospace, monospace'
const MONO_S = '400 10px "IBM Plex Mono", ui-monospace, monospace'
const SANS = '600 12px "IBM Plex Sans", system-ui, sans-serif'
const FPS = 30
const G = 0.7 // the whole scene is ghosted to this

const clamp = (x: number) => (x < 0 ? 0 : x > 1 ? 1 : x)
const ease = (x: number) => 1 - (1 - clamp(x)) ** 3
const span = (p: number, r: readonly [number, number]) => clamp((p - r[0]) / (r[1] - r[0]))
const rgba = (c: RGB, a: number) => `rgba(${c[0]},${c[1]},${c[2]},${clamp(a).toFixed(3)})`

export function isLowPower(nav: { hardwareConcurrency?: number; deviceMemory?: number }) {
  return (nav.hardwareConcurrency ?? 8) <= 4 || (nav.deviceMemory ?? 8) <= 4
}

/** A rounded orthogonal route through corner points, sampled for drawing and for moving along. */
function route(pts: [number, number][], r = 12): Path {
  const xs: number[] = []
  const ys: number[] = []
  const add = (x: number, y: number) => (xs.push(x), ys.push(y))
  add(...pts[0])
  for (let i = 1; i < pts.length; i++) {
    const [x0, y0] = pts[i - 1]
    const [x1, y1] = pts[i]
    const next = pts[i + 1]
    const len = Math.hypot(x1 - x0, y1 - y0) || 1
    const rr = next ? Math.min(r, len / 2, Math.hypot(next[0] - x1, next[1] - y1) / 2) : 0
    const ex = x1 - ((x1 - x0) / len) * rr
    const ey = y1 - ((y1 - y0) / len) * rr
    add(ex, ey)
    if (next && rr > 0) {
      const nl = Math.hypot(next[0] - x1, next[1] - y1) || 1
      const fx = x1 + ((next[0] - x1) / nl) * rr
      const fy = y1 + ((next[1] - y1) / nl) * rr
      for (let k = 1; k <= 5; k++) {
        const u = k / 5
        const v = 1 - u
        add(v * v * ex + 2 * v * u * x1 + u * u * fx, v * v * ey + 2 * v * u * y1 + u * u * fy)
      }
    }
  }
  const ls = [0]
  for (let i = 1; i < xs.length; i++) ls.push(ls[i - 1] + Math.hypot(xs[i] - xs[i - 1], ys[i] - ys[i - 1]))
  return { xs, ys, ls, total: ls[ls.length - 1] || 1 }
}

function along(p: Path, u: number): [number, number] {
  const d = clamp(u) * p.total
  let i = 1
  while (i < p.ls.length - 1 && p.ls[i] < d) i++
  const seg = p.ls[i] - p.ls[i - 1] || 1
  const k = (d - p.ls[i - 1]) / seg
  return [p.xs[i - 1] + (p.xs[i] - p.xs[i - 1]) * k, p.ys[i - 1] + (p.ys[i] - p.ys[i - 1]) * k]
}

export type AmbientEnv = { win?: Window & typeof globalThis; doc?: Document; start?: number }
export type Ambient = {
  mode: () => 'live' | 'static'
  layout: () => 'cards' | 'strip' | 'none'
  running: () => boolean
  frames: () => number
  time: () => number
  stop: () => void
}

export function startAmbient(host: HTMLElement, env: AmbientEnv = {}): Ambient {
  const win = env.win ?? window
  const doc = env.doc ?? document
  const nav = win.navigator as Navigator & { deviceMemory?: number }
  const low = isLowPower(nav)
  const reduceMq = win.matchMedia('(prefers-reduced-motion: reduce)')
  const hero = doc.querySelector<HTMLElement>('.nx-hero')
  const canvas = doc.createElement('canvas')
  const ctx = canvas.getContext('2d')
  let frames = 0
  if (!ctx || !hero) return { mode: () => 'static', layout: () => 'none', running: () => false, frames: () => 0, time: () => 0, stop: () => undefined }
  const c = ctx
  canvas.className = 'nx-scene'
  canvas.setAttribute('aria-hidden', 'true')
  hero.appendChild(canvas)

  // ---------- state ----------
  let W = 0
  let H = 0
  let t = env.start ?? 0.4 // scene seconds; only advances while running
  let mode: 'live' | 'static' = reduceMq.matches ? 'static' : 'live'
  let layout: 'cards' | 'strip' | 'none' = 'none'
  let mw = 6.3 // width of a mono character at 10.5px
  let nextMeasure = 0
  // cards
  let A: Box, M: Box, B: Box, C: Box
  let up: Path, down: Path, under: Path, back: Path
  // strip
  let sx0 = 0
  let sx1 = 0
  let sy = 0
  let caption = false

  // ---------- layout: from the measured text, in hero coordinates ----------
  function box(el: Element, hx: number, hy: number) {
    let r: DOMRect | undefined
    if (typeof doc.createRange === 'function') {
      const range = doc.createRange()
      range.selectNodeContents(el)
      r = range.getBoundingClientRect()
    }
    if (!r || !r.width) r = el.getBoundingClientRect()
    return { l: r.left - hx, t: r.top - hy, r: r.right - hx, b: r.bottom - hy, w: r.width }
  }

  function measure() {
    const hr = hero!.getBoundingClientRect()
    const w = Math.round(hero!.clientWidth || hr.width)
    if (!w) return void (layout = 'none')
    const texts = Array.from(doc.querySelectorAll('.nx-hero-copy > *'), (el) => box(el, hr.left, hr.top)).filter((b) => b.w > 0)
    const flow = doc.querySelector('.nx-flow')
    if (!texts.length) return void (layout = 'none')
    const L = Math.min(...texts.map((b) => b.l))
    const R = Math.max(...texts.map((b) => b.r))
    const top = Math.min(...texts.map((b) => b.t))
    const bottom = Math.max(...texts.map((b) => b.b))
    const flowTop = flow ? flow.getBoundingClientRect().top - hr.top : bottom + 80
    const h = Math.round(Math.max(bottom + 20, flowTop))
    c.font = MONO
    mw = c.measureText('0').width || 6.3

    const gut = Math.min(L, w - R)
    const laneY = Math.max(12, top / 2)
    const busY = (bottom + flowTop) / 2
    const s = Math.min(1, (gut - 64) / CW)
    const need = 24 + SH * s + 60 + CH * s + 24
    if (s >= 0.86 && busY - laneY >= need) {
      layout = 'cards'
      const cw = CW * s
      const xl = (gut - cw) / 2
      const xr = w - xl - cw
      const yTop = laneY + 24
      A = { x: xl, y: yTop, w: cw, h: SH * s, s }
      M = { x: xr, y: yTop, w: cw, h: MH * s, s }
      B = { x: xr, y: busY - 26 - SH * s, w: cw, h: SH * s, s }
      C = { x: xl, y: busY - 26 - CH * s, w: cw, h: CH * s, s }
      const d = Math.min(22, (L - (A.x + A.w)) / 2)
      const hy = 17 * s
      up = route([[A.x + A.w, A.y + hy], [A.x + A.w + d, A.y + hy], [A.x + A.w + d, laneY], [M.x - d, laneY], [M.x - d, M.y + hy], [M.x, M.y + hy]])
      down = route([[M.x + M.w / 2, M.y + M.h], [B.x + B.w / 2, B.y]])
      under = route([[B.x + B.w / 2, B.y + B.h], [B.x + B.w / 2, busY], [C.x + C.w / 2, busY], [C.x + C.w / 2, C.y + C.h]])
      back = route([[C.x + C.w / 2, C.y], [A.x + A.w / 2, A.y + A.h]])
    } else {
      // One strip under the buttons, in the gap above the pipeline.
      const lane = flowTop - bottom
      layout = lane >= 16 ? 'strip' : 'none'
      caption = lane >= 34
      const sw = Math.min(w - 32, 560)
      sx0 = (w - sw) / 2
      sx1 = sx0 + sw
      sy = bottom + (caption ? Math.min(15, lane / 2 - 4) : lane / 2)
    }
    if (w !== W || h !== H) {
      W = w
      H = h
      const dpr = Math.min(win.devicePixelRatio || 1, low ? 1.5 : 2)
      canvas.width = Math.round(W * dpr)
      canvas.height = Math.round(H * dpr)
      canvas.style.width = `${W}px`
      canvas.style.height = `${H}px`
      c.setTransform(dpr, 0, 0, dpr, 0, 0)
    }
  }

  // ---------- drawing primitives ----------
  function rr(x: number, y: number, w: number, h: number, r: number) {
    c.beginPath()
    c.moveTo(x + r, y)
    c.arcTo(x + w, y, x + w, y + h, r)
    c.arcTo(x + w, y + h, x, y + h, r)
    c.arcTo(x, y + h, x, y, r)
    c.arcTo(x, y, x + w, y, r)
    c.closePath()
  }
  function dot(x: number, y: number, r: number, col: RGB, a = 1) {
    c.fillStyle = rgba(col, a)
    c.beginPath()
    c.arc(x, y, r, 0, 6.2832)
    c.fill()
  }
  /** Text in coloured runs, left to right; returns the end x. */
  function runs(x: number, y: number, parts: [string, RGB, number?][]) {
    for (const [s, col, a = 1] of parts) {
      c.fillStyle = rgba(col, a)
      c.fillText(s, x, y)
      x += s.length * mw
    }
    return x
  }
  function panel(b: Box, h: number, env: number, glow: number) {
    c.save()
    c.translate(b.x, b.y + (1 - env) * 6)
    c.scale(b.s, b.s)
    c.globalAlpha = G * env
    rr(0.5, 0.5, CW - 1, h - 1, 10)
    c.fillStyle = rgba(PANEL, 0.9)
    c.fill()
    c.strokeStyle = rgba(RULE, 1)
    c.lineWidth = 1
    c.stroke()
    if (glow > 0.01) {
      c.strokeStyle = rgba(AMBER, 0.7 * glow)
      c.stroke()
    }
    c.fillStyle = rgba(RULE, 0.8)
    c.fillRect(0, 31, CW, 1)
    c.textBaseline = 'middle'
  }
  /** A card's title row: an agent dot or the Nexus tile, a name, and a note on the right (with the writer's dot). */
  function header(name: string, mark: RGB | 'nexus' | null, right: string, rdot?: RGB) {
    if (mark === 'nexus') {
      rr(9, 9, 14, 14, 3.5)
      c.fillStyle = rgba(AMBER, 1)
      c.fill()
      c.font = '700 9.5px "IBM Plex Sans", system-ui, sans-serif'
      c.fillStyle = rgba(PANEL, 1)
      c.fillText('N', 12.6, 16.5)
    } else if (mark) dot(15, 16, 3.2, mark)
    c.font = mark ? SANS : MONO
    c.fillStyle = rgba(mark ? INK : INK2, 0.92)
    c.fillText(name, mark === 'nexus' ? 29 : mark ? 25 : 14, 16.5)
    c.font = MONO_S
    c.fillStyle = rgba(INK3, 1)
    c.textAlign = 'right'
    c.fillText(right, CW - 12, 16.5)
    c.textAlign = 'left'
    if (rdot) dot(CW - 19 - c.measureText(right).width, 16, 2.6, rdot)
    c.font = MONO
  }
  function chip(x: number, y: number, key: string, rest: string, a: number) {
    if (a < 0.01) return
    c.font = MONO
    const w = (key.length + rest.length) * mw + 18
    c.globalAlpha = G * a
    rr(x - w / 2, y - 10, w, 20, 10)
    c.fillStyle = rgba([30, 27, 20], 0.97)
    c.fill()
    c.strokeStyle = rgba(AMBER, 0.65)
    c.lineWidth = 1
    c.stroke()
    c.textBaseline = 'middle'
    runs(x - w / 2 + 9, y + 0.5, [[key, AMBER], [rest, INK2]])
    c.globalAlpha = 1
  }
  function wire(p: Path) {
    c.beginPath()
    c.moveTo(p.xs[0], p.ys[0])
    for (let i = 1; i < p.xs.length; i++) c.lineTo(p.xs[i], p.ys[i])
    c.stroke()
  }
  /** A signal along a path: an amber trail behind a chip. */
  function travel(p: Path, u: number, key: string, rest: string, rev = false) {
    if (u <= 0 || u >= 1) return
    const a = Math.min(1, u * 8, (1 - u) * 8)
    const uu = rev ? 1 - u : u
    const [x, y] = along(p, uu)
    const [tx, ty] = along(p, rev ? Math.min(1, uu + 0.3) : Math.max(0, uu - 0.3))
    const g = c.createLinearGradient(tx, ty, x, y)
    g.addColorStop(0, rgba(AMBER, 0))
    g.addColorStop(1, rgba(AMBER, 0.75 * G * a))
    c.strokeStyle = g
    c.lineWidth = 1.5
    c.setLineDash([])
    // Only the stretch behind the chip.
    const d0 = Math.min(uu, rev ? Math.min(1, uu + 0.3) : Math.max(0, uu - 0.3)) * p.total
    const d1 = Math.max(uu, rev ? Math.min(1, uu + 0.3) : Math.max(0, uu - 0.3)) * p.total
    c.beginPath()
    c.moveTo(...along(p, d0 / p.total))
    for (let i = 0; i < p.xs.length; i++) if (p.ls[i] > d0 && p.ls[i] < d1) c.lineTo(p.xs[i], p.ys[i])
    c.lineTo(...along(p, d1 / p.total))
    c.stroke()
    c.lineWidth = 1
    chip(x, y, key, rest, a)
  }

  const typed = (s: string, k: number) => s.slice(0, Math.round(s.length * k))
  const caret = (x: number, y: number, on: boolean) => {
    if (!on || Math.floor(t * 2.4) % 2) return
    c.fillStyle = rgba(AMBER, 0.9)
    c.fillRect(x + 1, y - 6, mw * 0.6, 12)
  }
  const redacted = (st: Story, p: number) => (st.secret && p >= P.cap ? st.say.slice(0, st.secret[0]) + '•'.repeat(st.secret[1] - st.secret[0]) + st.say.slice(st.secret[1]) : st.say)

  // ---------- the cards ----------
  function drawCards(p: number, k: number) {
    const st = STORIES[k % STORIES.length]
    const a = AGENTS[st.a]
    const b = AGENTS[st.b]
    const out = 1 - ease((p - P.out) / 0.8)

    // The ring of wires, always there, faint.
    c.strokeStyle = rgba(INK, 0.2 * G)
    c.lineWidth = 1
    c.setLineDash([1.5, 5])
    for (const r of [up, down, under, back]) wire(r)
    c.setLineDash([])

    // 1. The chat the decision comes from.
    const aEnv = ease(p / 0.5) * out
    if (aEnv > 0.01) {
      panel(A, SH, aEnv, !st.undo && p >= P.back[1] ? 1 - ease((p - P.back[1]) / 1.2) : 0)
      header(a.name, a.rgb, 'session')
      const say = redacted(st, p)
      const k1 = span(p, P.type)
      c.fillStyle = rgba(INK3, 1)
      c.fillText('›', 14, 50)
      if (st.secret && p >= P.cap) {
        const [i, j] = st.secret
        runs(26, 50, [[say.slice(0, i), INK2], [say.slice(i, j), AMBER, 0.85], [say.slice(j), INK2]])
      } else runs(26, 50, [[typed(say, k1), INK2]])
      caret(26 + typed(say, k1).length * mw, 50, p > P.type[0] - 0.3 && p < P.ok)
      const okA = ease((p - P.ok) / 0.4)
      if (okA > 0) {
        dot(17, 71, 2.4, a.rgb, okA)
        runs(26, 71, [[st.ok, INK2, okA]])
      }
      // Captured: a sweep under the line, then the note.
      const cap = p - P.cap
      if (cap > 0) {
        const sweep = ease(cap / 0.45) * (1 - ease((p - P.up[1]) / 0.8))
        c.fillStyle = rgba(AMBER, 0.6 * sweep)
        c.fillRect(26, 58, say.length * mw * ease(cap / 0.45), 1)
        const note = st.secret && p < P.cap + 0.9 ? '↳ secret redacted' : st.secret ? '↳ redacted · decision saved' : '↳ decision captured'
        runs(14, 93, [[note, AMBER, ease(cap / 0.4) * 0.9]])
      }
      c.restore()
    }

    // Memory: always on screen; it keeps what earlier stories saved.
    const items: [string, string, number][] = []
    for (let i = k; i >= 0 && items.length < 4; i--) {
      const s = STORIES[i % STORIES.length]
      if (i === k && p < P.mem) continue
      items.push(['Decision', s.decision, s.a])
    }
    for (const sd of SEED) if (items.length < 4) items.push(sd)
    const arrived = p >= P.mem
    const e = arrived ? ease((p - P.mem) / 0.5) : 0
    panel(M, MH, 1, arrived ? 1 - ease((p - P.mem) / 1.4) : 0)
    header('Nexus', 'nexus', 'shared memory')
    c.save()
    rr(0, 32, CW, MH - 32, 10)
    c.clip()
    items.forEach(([kind, text, ag], i) => {
      // The new item fades in on top while the others slide down a row.
      const isNew = arrived && i === 0
      const y = isNew ? 50 : 50 + 20 * (arrived ? i - 1 + e : i)
      const al = isNew ? e : arrived && i === 3 ? 1 - e : i < 3 ? 1 : 0
      if (al < 0.01) return
      if (isNew) {
        const wash = 1 - ease((p - P.mem - 1.2) / 2)
        c.fillStyle = rgba(AMBER, 0.12 * wash * e)
        c.fillRect(6, y - 9, CW - 12, 18)
      }
      runs(14, y, [[kind, AMBER, 0.9 * al], [' · ', INK3, al], [text, INK2, al]])
      dot(CW - 16, y, 2.4, AGENTS[ag].rgb, al)
    })
    c.restore()
    c.restore()

    // 2-3. Memory flows to the next agent's new session.
    travel(up, span(p, P.up), 'Decision', ` · ${st.decision}`)
    travel(down, span(p, P.down), 'context', '')
    const bEnv = ease((p - P.bIn) / 0.5) * out
    if (bEnv > 0.01) {
      panel(B, SH, bEnv, p >= P.ctx ? 1 - ease((p - P.ctx) / 1.4) : 0)
      header(b.name, b.rgb, 'new session')
      const ca = ease((p - P.ctx) / 0.4)
      if (ca > 0) {
        const label = `context loaded · from ${a.name}`
        rr(10, 41, label.length * mw + 26, 18, 9)
        c.fillStyle = rgba(AMBER, 0.13 * ca)
        c.fill()
        dot(19, 50, 2.4, a.rgb, ca)
        runs(26, 50.5, [['context loaded', AMBER, ca], [` · from ${a.name}`, INK2, ca]])
      }
      const k2 = span(p, P.ask)
      if (p > P.ask[0]) {
        c.fillStyle = rgba(INK3, 1)
        c.fillText('›', 14, 71)
        const s = typed(st.ask, k2)
        runs(26, 71, [[s, INK2]])
        caret(26 + s.length * mw, 71, p < P.reply)
      }
      const ra = ease((p - P.reply) / 0.4)
      if (ra > 0) {
        dot(17, 92, 2.4, b.rgb, ra)
        runs(26, 92, [[st.reply, INK2, ra]])
      }
      c.restore()
    }

    // 4. The code it wrote, and the line's why.
    travel(under, span(p, P.under), `${st.file}:${st.line}`, '')
    const cEnv = ease((p - P.cIn) / 0.5) * out
    if (cEnv > 0.01) {
      const undone = st.undo && p >= P.back[0] + 0.7
      panel(C, CH, cEnv, p >= P.code[0] ? 1 - ease((p - P.code[0]) / 1.2) : 0)
      header(st.file, null, b.name, b.rgb)
      c.font = MONO
      const kc = span(p, P.code)
      st.code.forEach((line, i) => {
        const y = 50 + i * 19
        const n = st.line + i - 1
        const hot = n === st.line
        if (hot) {
          c.fillStyle = rgba(AMBER, 0.1)
          c.fillRect(1, y - 9, CW - 2, 18)
          c.fillStyle = rgba(AMBER, 0.8)
          c.fillRect(1, y - 9, 2, 18)
        }
        c.textAlign = 'right'
        c.fillStyle = rgba(INK3, 1)
        c.fillText(String(n), 32, y)
        c.textAlign = 'left'
        const s = typed(line, clamp(kc * 2 - i))
        runs(42, y, [[s, INK2, undone ? 0.4 : 1]])
        if (undone) {
          c.fillStyle = rgba(INK3, 0.8)
          c.fillRect(42, y, s.length * mw, 1)
        }
      })
      const wa = ease((p - P.why) / 0.4)
      if (wa > 0) {
        runs(14, 97, [['why', AMBER, wa], [` ← chat with ${b.name}`, INK2, wa]])
        const x = runs(14, 114, [['    decision from ', INK3, wa]])
        dot(x + 3, 114, 2.4, a.rgb, wa)
        runs(x + 10, 114, [[a.name, INK2, wa]])
      }
      // Keep or undo, like the review panel.
      c.font = MONO_S
      const btn = (label: string, x: number, lit: number) => {
        rr(x, 124, label.length * 6 + 14, 12, 6)
        c.strokeStyle = rgba(lit > 0.01 ? AMBER : RULE, lit > 0.01 ? 0.4 + 0.6 * lit : 1)
        c.stroke()
        c.fillStyle = rgba(lit > 0.01 ? AMBER : INK3, 1)
        c.fillText(label, x + 7, 130.5)
      }
      const litUndo = st.undo ? clamp((p - P.back[0] - 0.3) / 0.3) * (1 - ease((p - P.out) / 0.6)) : 0
      c.fillStyle = rgba(undone ? AMBER : INK3, 1)
      c.fillText(undone ? 'change undone' : '1 file changed', 14, 130.5)
      btn('Undo', CW - 96, litUndo)
      btn('Keep', CW - 50, 0)
      c.font = MONO
      c.restore()
    }
    // The why, traced back to the chat it came from.
    if (!st.undo) travel(back, span(p, P.back), 'why', '')
  }

  // ---------- the strip (phones, tablets) ----------
  function drawStrip(p: number, k: number) {
    const st = STORIES[k % STORIES.length]
    const a = AGENTS[st.a]
    const b = AGENTS[st.b]
    const names = Math.min(ease(p / 0.6), 1 - ease((p - 15.4) / 0.6))
    c.textBaseline = 'middle'
    c.font = MONO
    c.globalAlpha = G
    // Agent → Nexus → agent.
    dot(sx0 + 4, sy, 3, a.rgb, names)
    runs(sx0 + 12, sy, [[a.name, INK2, names]])
    const aEnd = sx0 + 12 + a.name.length * mw + 8
    const bw = b.name.length * mw
    dot(sx1 - bw - 8, sy, 3, b.rgb, names)
    runs(sx1 - bw, sy, [[b.name, INK2, names]])
    const bStart = sx1 - bw - 16
    const mid = (sx0 + sx1) / 2
    const nw = 14 + 6 + 5 * mw
    rr(mid - nw / 2, sy - 7, 14, 14, 3.5)
    c.fillStyle = rgba(AMBER, 0.95)
    c.fill()
    c.font = '700 9.5px "IBM Plex Sans", system-ui, sans-serif'
    c.fillStyle = rgba(PANEL, 1)
    c.fillText('N', mid - nw / 2 + 3.6, sy + 0.5)
    c.font = MONO
    runs(mid - nw / 2 + 20, sy, [['Nexus', INK, 0.9]])
    const l = route([[aEnd, sy], [mid - nw / 2 - 8, sy]])
    const r = route([[mid + nw / 2 + 8, sy], [bStart, sy]])
    c.strokeStyle = rgba(INK, 0.25)
    c.setLineDash([1.5, 5])
    wire(l)
    wire(r)
    c.setLineDash([])
    c.globalAlpha = 1
    travel(l, span(p, P.up), '', '')
    travel(r, span(p, P.down), '', '')
    // What is happening, in words.
    if (!caption) return
    const steps: [number, [string, RGB][]][] = [
      [0, [['› ', INK3], [redacted(st, p).slice(0, Math.max(1, Math.round(st.say.length * span(p, P.type)))), INK2]]],
      [P.cap, st.secret ? [['secret redacted', AMBER], [' · key never saved', INK2]] : [['decision captured', AMBER], [` · ${a.name}`, INK2]]],
      [P.mem, [['memory', AMBER], [` · ${st.decision}`, INK2]]],
      [P.ctx, [[b.name, INK2], [' · context loaded', AMBER], [` from ${a.name}`, INK2]]],
      [P.cIn, [[`${st.file}:${st.line}`, INK2], [' · why ', AMBER], [`← chat with ${b.name}`, INK2]]],
    ]
    let i = steps.length - 1
    while (i > 0 && p < steps[i][0]) i--
    const next = steps[i + 1]?.[0] ?? P.out + 0.6
    const ca = Math.min(ease((p - steps[i][0]) / 0.35), 1 - ease((p - next + 0.3) / 0.3))
    const parts = steps[i][1]
    c.font = MONO_S
    const cw = c.measureText('0').width || 6
    const len = parts.reduce((n, [s]) => n + s.length, 0) * cw
    let x = Math.max(sx0, (sx0 + sx1) / 2 - len / 2)
    c.globalAlpha = G * ca
    for (const [s, col] of parts) {
      c.fillStyle = rgba(col, col === INK3 ? 1 : 0.95)
      c.fillText(s, x, sy + 18)
      x += s.length * cw
    }
    c.globalAlpha = 1
  }

  function draw() {
    frames++
    c.clearRect(0, 0, W, H)
    if (layout === 'none' || !W) return
    const k = Math.floor(t / CYCLE)
    const p = t - k * CYCLE
    c.save()
    if (layout === 'cards') drawCards(p, k)
    else drawStrip(p, k)
    c.restore()
  }

  // ---------- running ----------
  let raf = 0
  let last = 0
  let hidden = doc.visibilityState === 'hidden'
  let inView = true
  let dialogOpen = false
  let stopped = false
  const player = doc.querySelector<HTMLDialogElement>('dialog[data-player]')
  const live = () => mode === 'live' && !hidden && inView && !dialogOpen && !stopped

  function loop(now: number) {
    raf = win.requestAnimationFrame(loop)
    if (!last) last = now - 1000 / FPS
    if (now - last < 1000 / FPS - 2) return
    const dt = Math.min(0.1, (now - last) / 1000)
    last = now
    if (player?.open) {
      // The film covers the page: rest until it closes.
      dialogOpen = true
      player.addEventListener('close', () => {
        dialogOpen = false
        update()
      }, { once: true })
      return update()
    }
    t += dt
    if (t >= nextMeasure) {
      nextMeasure = t + 2
      measure()
    }
    draw()
  }

  function update() {
    if (live()) {
      if (!raf) {
        last = 0
        raf = win.requestAnimationFrame(loop)
      }
    } else if (raf) {
      win.cancelAnimationFrame(raf)
      raf = 0
    }
  }

  const redraw = () => {
    measure()
    if (mode === 'static') draw()
  }
  let resizeTimer: ReturnType<typeof setTimeout> | undefined
  const onResize = () => {
    clearTimeout(resizeTimer)
    resizeTimer = setTimeout(redraw, 200)
  }
  const onVisibility = () => {
    hidden = doc.visibilityState === 'hidden'
    update()
  }
  const onMotionPref = () => {
    mode = reduceMq.matches ? 'static' : 'live'
    if (mode === 'static') t = STILL_AT
    update()
    if (mode === 'static') draw()
  }

  win.addEventListener('resize', onResize, { passive: true })
  doc.addEventListener('visibilitychange', onVisibility)
  reduceMq.addEventListener?.('change', onMotionPref)
  const io = win.IntersectionObserver
    ? new win.IntersectionObserver((entries) => {
        inView = entries[entries.length - 1].isIntersecting
        host.classList.toggle('is-past', !inView)
        update()
      })
    : null
  io?.observe(canvas)
  // Web fonts change the text boxes the scene is placed around.
  void doc.fonts?.ready.then(() => !stopped && redraw())

  if (mode === 'static') t = STILL_AT
  measure()
  draw()
  host.classList.add('is-live')
  update()

  return {
    mode: () => mode,
    layout: () => layout,
    running: () => raf !== 0,
    frames: () => frames,
    time: () => t,
    stop() {
      stopped = true
      update()
      clearTimeout(resizeTimer)
      win.removeEventListener('resize', onResize)
      doc.removeEventListener('visibilitychange', onVisibility)
      reduceMq.removeEventListener?.('change', onMotionPref)
      io?.disconnect()
      canvas.remove()
      host.classList.remove('is-live', 'is-past')
    },
  }
}

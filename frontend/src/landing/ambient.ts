/*
 * The living background behind the landing page (the .nx-ambient layer).
 *
 * A faint field of points drifts and connects; the six agents sit in the
 * hero as small clusters in their own colours, wired into the headline the
 * way the "how it works" diagram wires them into Nexus; amber pulses run
 * along the connections, and now and then a tiny note ("decision saved",
 * "why: retry.ts:15") fades in at a node and out again.
 *
 * Canvas 2D at about 30 fps, loaded after first paint by client.ts. It pauses
 * while the tab is hidden, the layer is not rendered or the film player is
 * open; small screens and low-power devices get fewer points; with reduced
 * motion it draws one still frame. Everything fades near the page's text
 * blocks, so text keeps its contrast. Decoration only: the layer is
 * aria-hidden and takes no pointer events (site.css).
 */

type RGB = readonly [number, number, number]
type Rect = { l: number; t: number; r: number; b: number }
type Label = { at: () => [number, number] | null; key: string; rest: string; age: number; life: number; side: 1 | -1; w: number }

const INK: RGB = [236, 235, 230]
const AMBER: RGB = [232, 187, 103]
// The agent dots from DESIGN.md, in the pipeline's order.
const AGENTS: { name: string; rgb: RGB }[] = [
  { name: 'Claude Code', rgb: [217, 137, 91] },
  { name: 'Codex', rgb: [154, 167, 240] },
  { name: 'Cursor', rgb: [191, 197, 194] },
  { name: 'OpenCode', rgb: [127, 194, 166] },
  { name: 'Gemini', rgb: [122, 168, 240] },
  { name: 'Kimi', rgb: [197, 154, 230] },
]
// Notes: an amber key and an optional plain rest.
const SAVED: [string, string][] = [['decision saved', ''], ['memory saved', ' · retry rules'], ['secret redacted', ''], ['brief updated', '']]
const NOTES: [string, string][] = [['why:', ' retry.ts:15'], ['line 42', ' ← chat #318'], ['decision saved', ''], ['memory:', ' capture retries'], ['why:', ' auth.go:88'], ['context loaded', '']]

// Text the layer keeps clear of, and the opaque panels (pipeline, tabs, film band), so notes never hide under them.
const QUIET = '.nx-hero-copy > *, .nx-section-head, .nx-tabs, .nx-faq-grid > *, .nx-final-in > *, .nx-foot-grid > *, .nx-flow, .nx-panels, .nx-more'
const QUIET_PAD = 64 // px around a text block over which the layer fades back in
const QUIET_MIN = 0.22 // what is left of it right behind text
const FPS = 30
const PARALLAX = 0.25 // the field moves at a quarter of the scroll speed

const rgba = (c: RGB, a: number) => `rgba(${c[0]},${c[1]},${c[2]},${Math.max(0, Math.min(1, a)).toFixed(3)})`
const mod = (a: number, m: number) => ((a % m) + m) % m
const smooth = (x: number) => (x <= 0 ? 0 : x >= 1 ? 1 : x * x * (3 - 2 * x))

/** A small seeded generator, so a still frame is the same every time. */
function mulberry32(seed: number) {
  let s = seed >>> 0
  return () => {
    s = (s + 0x6d2b79f5) >>> 0
    let t = s
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

export function isLowPower(nav: { hardwareConcurrency?: number; deviceMemory?: number }) {
  return (nav.hardwareConcurrency ?? 8) <= 4 || (nav.deviceMemory ?? 8) <= 4
}

/** How many drifting points: by screen area, fewer on low-power devices. */
export function pointBudget(w: number, h: number, low: boolean) {
  const n = Math.round(Math.min(78, Math.max(18, (w * h) / 17000)))
  return low ? Math.round(n * 0.6) : n
}

export type AmbientEnv = { win?: Window & typeof globalThis; doc?: Document; seed?: number }
export type Ambient = {
  mode: () => 'live' | 'static'
  running: () => boolean
  frames: () => number
  points: () => number
  stop: () => void
}

export function startAmbient(host: HTMLElement, env: AmbientEnv = {}): Ambient {
  const win = env.win ?? window
  const doc = env.doc ?? document
  const nav = win.navigator as Navigator & { deviceMemory?: number }
  const low = isLowPower(nav)
  const reduceMq = win.matchMedia('(prefers-reduced-motion: reduce)')
  const finePointer = !low && win.matchMedia('(pointer: fine)').matches
  const rand = mulberry32(env.seed ?? Math.floor(Math.random() * 2 ** 32))
  const between = (a: number, b: number) => a + rand() * (b - a)
  const pick = <T>(xs: readonly T[]) => xs[Math.floor(rand() * xs.length)]

  const canvas = doc.createElement('canvas')
  canvas.className = 'nx-ambient-canvas'
  canvas.setAttribute('aria-hidden', 'true')
  const ctx = canvas.getContext('2d')
  let frames = 0
  if (!ctx) return { mode: () => 'static', running: () => false, frames: () => 0, points: () => 0, stop: () => undefined }
  host.appendChild(canvas)

  // ---------- state ----------
  let W = 0
  let H = 0
  let D = 140 // link distance
  let tileW = 1
  let tileH = 1
  let t = 0 // simulation seconds; only advances while running
  let mode: 'live' | 'static' = reduceMq.matches ? 'static' : 'live'
  let phone = false
  let scroll = 0
  let quiet: Rect[] = [] // page coordinates
  let vq: Rect[] = [] // this frame, viewport coordinates
  let hub = { x: 0, y: 0 } // page coordinates: the middle of the headline
  let nextMeasure = 0

  type Node = { x: number; y: number; vx: number; vy: number; w: number; ph: number; wob: number; r: number; flare: number; sx: number; sy: number; q: number; hot: number }
  const field: Node[] = []
  const newNode = (): Node => {
    const a = rand() * Math.PI * 2
    const v = between(3, 8)
    return { x: rand() * tileW, y: rand() * tileH, vx: Math.cos(a) * v, vy: Math.sin(a) * v, w: between(0.08, 0.22), ph: rand() * 6.3, wob: between(2, 6), r: between(0.8, 1.5), flare: 0, sx: 0, sy: 0, q: 1, hot: 0 }
  }

  type Sat = { r: number; w: number; ph: number; br: number; x: number; y: number }
  type Agent = { rgb: RGB; name: string; hx: number; hy: number; w1: number; w2: number; p1: number; p2: number; a1: number; a2: number; sats: Sat[]; x: number; y: number; q: number; flare: number }
  const agents: Agent[] = AGENTS.map((a) => ({
    rgb: a.rgb, name: a.name, hx: 0, hy: 0,
    w1: between(0.08, 0.15), w2: between(0.21, 0.37), p1: rand() * 6.3, p2: rand() * 6.3, a1: between(22, 44), a2: between(8, 18),
    sats: [], x: 0, y: 0, q: 1, flare: 0,
  }))

  type FieldPulse = { a: number; b: number; t: number; dur: number; hops: number }
  type WirePulse = { k: number; out: boolean; t: number; dur: number; then?: () => void }
  const fieldPulses: FieldPulse[] = []
  const wirePulses: WirePulse[] = []
  const labels: Label[] = []
  let nextField = 1.2
  let nextWire = 2
  let nextStory = 3.5
  let lastLabel = -10

  let cx = 0
  let cy = 0
  let px = 0
  let py = 0
  let pointerOn = false
  let pointerAmt = 0

  // ---------- layout ----------
  function measureQuiet() {
    const sy = win.scrollY
    quiet = Array.from(doc.querySelectorAll<HTMLElement>(QUIET), (el) => {
      const r = el.getBoundingClientRect()
      return { l: r.left, t: r.top + sy, r: r.right, b: r.bottom + sy }
    }).filter((r) => r.r > r.l && r.b > r.t)
    const h1 = doc.querySelector('.nx-hero-copy h1')?.getBoundingClientRect()
    hub = h1 && h1.width ? { x: h1.left + h1.width / 2, y: h1.top + sy + h1.height / 2 } : { x: W / 2, y: H * 0.36 }
  }

  function layoutAgents() {
    const rx = phone ? W / 2 + 4 : Math.min(W * 0.36, 560)
    const ry = phone ? 170 : Math.min(H * 0.3, 240)
    // Three a side, staggered so the two sides never mirror each other.
    const left: [number, number][] = [[0.88, -0.62], [1, 0.04], [0.8, 0.72]]
    const right: [number, number][] = [[0.95, -0.48], [0.8, 0.22], [1.02, 0.86]]
    const sats = phone ? 2 : low ? 3 : 4
    agents.forEach((a, i) => {
      const [fx, fy] = i < 3 ? left[i] : right[i - 3]
      a.hx = (i < 3 ? -1 : 1) * rx * fx
      a.hy = ry * fy
      while (a.sats.length < sats) a.sats.push({ r: between(14, phone ? 26 : 40), w: between(0.05, 0.14) * (rand() < 0.5 ? -1 : 1), ph: rand() * 6.3, br: between(0.2, 0.5), x: 0, y: 0 })
      a.sats.length = sats
    })
  }

  function resize() {
    const w = Math.round(host.clientWidth || win.innerWidth)
    const h = Math.round(host.clientHeight || win.innerHeight)
    if (!w || !h) return
    const dpr = Math.min(win.devicePixelRatio || 1, low ? 1.5 : 2)
    const oldW = tileW
    const oldH = tileH
    W = w
    H = h
    phone = W < 700
    D = phone ? 112 : 140
    tileW = W + 2 * D
    tileH = H + 2 * D
    canvas.width = Math.round(W * dpr)
    canvas.height = Math.round(H * dpr)
    ctx!.setTransform(dpr, 0, 0, dpr, 0, 0)
    for (const n of field) {
      n.x *= tileW / oldW
      n.y *= tileH / oldH
    }
    const want = pointBudget(W, H, low)
    while (field.length < want) field.push(newNode())
    field.length = want
    fieldPulses.length = 0
    layoutAgents()
    measureQuiet()
  }

  // ---------- helpers ----------
  /** 1 in open space, down to QUIET_MIN right behind text. */
  function quietAt(x: number, y: number) {
    let q = 1
    for (const r of vq) {
      const dx = Math.max(r.l - x, 0, x - r.r)
      const dy = Math.max(r.t - y, 0, y - r.b)
      if (dx >= QUIET_PAD || dy >= QUIET_PAD) continue
      const d = Math.hypot(dx, dy)
      if (d < QUIET_PAD) q = Math.min(q, QUIET_MIN + (1 - QUIET_MIN) * smooth(d / QUIET_PAD))
    }
    return q
  }

  /** The curve from an agent to the hub, shaped like the pipeline's wires. */
  function wire(a: Agent, u: number): [number, number] {
    const hx = hub.x
    const hy = hub.y - scroll
    const x1 = a.x + (hx - a.x) * 0.55
    const x2 = hx - (hx - a.x) * 0.45
    const v = 1 - u
    return [
      v * v * v * a.x + 3 * v * v * u * x1 + 3 * v * u * u * x2 + u * u * u * hx,
      v * v * v * a.y + 3 * v * v * u * a.y + 3 * v * u * u * hy + u * u * u * hy,
    ]
  }

  const onScreen = (x: number, y: number, m = 0) => x > -m && x < W + m && y > -m && y < H + m

  function addLabel(at: () => [number, number] | null, note: [string, string]) {
    const max = W >= 1200 ? 2 : 1
    if (labels.length >= max || t - lastLabel < 3) return
    const p = at()
    if (!p) return
    ctx!.font = '400 11px "IBM Plex Mono", ui-monospace, monospace'
    const w = ctx!.measureText(note[0] + note[1]).width + 26
    // Only in open space: the whole note, on whichever side has room.
    const clear = (side: 1 | -1) => {
      const x0 = side > 0 ? p[0] - 6 : p[0] - w
      const x1 = side > 0 ? p[0] + w : p[0] + 6
      const y0 = p[1] - 24
      const y1 = p[1] + 8
      if (x0 < 12 || x1 > W - 12 || y0 < 72 || y1 > H - 12) return false
      for (const l of labels) {
        const q = l.at()
        if (q && Math.abs(q[1] - p[1]) < 40 && Math.abs(q[0] - p[0]) < w + l.w) return false
      }
      return [[x0, y0], [x1, y0], [x0, y1], [x1, y1], [(x0 + x1) / 2, (y0 + y1) / 2]].every(([x, y]) => quietAt(x, y) > 0.97)
    }
    const side = clear(1) ? 1 : clear(-1) ? -1 : 0
    if (!side) return
    labels.push({ at, key: note[0], rest: note[1], age: 0, life: 3.8, side, w })
    lastLabel = t
  }

  // ---------- story: an agent saves a decision, Nexus hands it to another ----------
  function story() {
    const seen = agents.map((_, k) => k).filter((k) => onScreen(agents[k].x, agents[k].y, -24))
    if (!seen.length) return
    const k = pick(seen)
    const a = agents[k]
    addLabel(() => [a.x, a.y], pick(SAVED))
    wirePulses.push({
      k, out: false, t: 0, dur: between(2.6, 3.4),
      then: () => {
        const others = agents.map((_, j) => j).filter((j) => j !== k)
        const j = pick(others)
        const b = agents[j]
        wirePulses.push({ k: j, out: true, t: 0, dur: between(2.6, 3.2), then: () => {
          b.flare = 1
          addLabel(() => [b.x, b.y], rand() < 0.6 ? ['context loaded', ` · from ${a.name}`] : ['picked up', ' 1 decision'])
        } })
      },
    })
  }

  // ---------- simulation ----------
  function step(dt: number) {
    t += dt
    for (const n of field) {
      n.x += (n.vx + Math.cos(t * n.w + n.ph) * n.wob) * dt
      n.y += (n.vy + Math.sin(t * n.w * 1.3 + n.ph) * n.wob) * dt
      n.flare = Math.max(0, n.flare - dt / 1.4)
    }
    for (const a of agents) a.flare = Math.max(0, a.flare - dt / 1.6)
    pointerAmt += ((pointerOn ? 1 : 0) - pointerAmt) * Math.min(1, dt * 3)
    cx += (px - cx) * Math.min(1, dt * 5)
    cy += (py - cy) * Math.min(1, dt * 5)

    for (let i = fieldPulses.length - 1; i >= 0; i--) {
      const p = fieldPulses[i]
      p.t += dt / p.dur
      if (p.t < 1) continue
      fieldPulses.splice(i, 1)
      const b = field[p.b]
      if (!b) continue
      b.flare = 1
      // Hop on to a neighbour now and then: a signal finding its way.
      const next = p.hops < 5 && rand() < 0.62 ? neighbour(p.b, p.a) : -1
      if (next >= 0) fieldPulses.push({ a: p.b, b: next, t: 0, dur: between(0.9, 1.5), hops: p.hops + 1 })
      else if (rand() < 0.35) addLabel(() => (field[p.b] ? [field[p.b].sx, field[p.b].sy] : null), pick(NOTES))
    }
    for (let i = wirePulses.length - 1; i >= 0; i--) {
      const p = wirePulses[i]
      p.t += dt / p.dur
      if (p.t < 1) continue
      wirePulses.splice(i, 1)
      p.then?.()
    }
    for (let i = labels.length - 1; i >= 0; i--) {
      labels[i].age += dt
      if (labels[i].age >= labels[i].life) labels.splice(i, 1)
    }

    if (t >= nextField) {
      nextField = t + (phone ? between(1.6, 3.4) : between(0.7, 1.9))
      if (fieldPulses.length < (phone ? 2 : 4)) spawnFieldPulse()
    }
    if (t >= nextWire) {
      nextWire = t + between(2.2, 4.5)
      if (wirePulses.length < 4) wirePulses.push({ k: Math.floor(rand() * agents.length), out: rand() < 0.3, t: 0, dur: between(2.6, 3.6) })
    }
    if (t >= nextStory) {
      nextStory = t + between(7, 12)
      story()
    }
  }

  function neighbour(i: number, not: number) {
    const n = field[i]
    const near: number[] = []
    field.forEach((m, j) => {
      if (j !== i && j !== not && Math.hypot(m.sx - n.sx, m.sy - n.sy) < D * 0.9 && onScreen(m.sx, m.sy)) near.push(j)
    })
    return near.length ? pick(near) : -1
  }

  function spawnFieldPulse() {
    for (let tries = 0; tries < 8; tries++) {
      const i = Math.floor(rand() * field.length)
      const n = field[i]
      if (!n || !onScreen(n.sx, n.sy, -20) || n.q < 0.6) continue
      const j = neighbour(i, -1)
      if (j >= 0) return void fieldPulses.push({ a: i, b: j, t: 0, dur: between(0.9, 1.5), hops: 0 })
    }
  }

  // ---------- drawing ----------
  const EDGE_BUCKETS = 6
  const buckets: number[][] = Array.from({ length: EDGE_BUCKETS }, () => [])

  function draw() {
    const c = ctx!
    frames++
    scroll = win.scrollY
    vq = quiet
      .map((r) => ({ l: r.l, t: r.t - scroll, r: r.r, b: r.b - scroll }))
      .filter((r) => r.b > -QUIET_PAD && r.t < H + QUIET_PAD)
    // Strongest in the hero, quieter once the page is scrolled.
    const g = 1 - 0.4 * smooth(scroll / Math.max(1, H))
    const par = mode === 'live' ? PARALLAX : 0
    c.clearRect(0, 0, W, H)

    // Points: the drifting field plus each agent's cluster, all of which can link up.
    const pull = finePointer ? pointerAmt : 0
    for (const n of field) {
      let x = mod(n.x, tileW) - D
      let y = mod(n.y - scroll * par, tileH) - D
      n.hot = 0
      if (pull > 0.01) {
        const dx = cx - x
        const dy = cy - y
        const d = Math.hypot(dx, dy)
        if (d < 180 && d > 1) {
          const f = (1 - d / 180) ** 2 * pull
          x += (dx / d) * f * 22
          y += (dy / d) * f * 22
          n.hot = f
        }
      }
      n.sx = x
      n.sy = y
      n.q = quietAt(x, y)
    }
    for (const a of agents) {
      a.x = hub.x + a.hx + Math.sin(t * a.w1 + a.p1) * a.a1 + Math.sin(t * a.w2 + a.p2) * a.a2
      a.y = hub.y - scroll + a.hy + Math.cos(t * a.w1 * 0.8 + a.p2) * a.a1 * 0.7 + Math.sin(t * a.w2 * 1.1 + a.p1) * a.a2
      a.q = quietAt(a.x, a.y)
      for (const s of a.sats) {
        const k = 1 + 0.14 * Math.sin(t * s.br + s.ph)
        s.x = a.x + Math.cos(t * s.w + s.ph) * s.r * k
        s.y = a.y + Math.sin(t * s.w + s.ph) * s.r * k * 0.8
      }
    }

    // Links between nearby points, batched by strength into a few strokes.
    for (const b of buckets) b.length = 0
    const pts: { x: number; y: number; q: number; hot: number; ag: number }[] = field.map((n) => ({ x: n.sx, y: n.sy, q: n.q, hot: n.hot, ag: -1 }))
    agents.forEach((a, k) => {
      if (!onScreen(a.x, a.y, 80)) return
      pts.push({ x: a.x, y: a.y, q: a.q, hot: 0, ag: k })
      for (const s of a.sats) pts.push({ x: s.x, y: s.y, q: a.q, hot: 0, ag: k })
    })
    const EDGE = 0.2
    for (let i = 0; i < pts.length; i++) {
      const p = pts[i]
      for (let j = i + 1; j < pts.length; j++) {
        const o = pts[j]
        if (p.ag >= 0 && p.ag === o.ag) continue // a cluster draws its own spokes
        const dx = o.x - p.x
        const dy = o.y - p.y
        if (dx > D || dx < -D || dy > D || dy < -D) continue
        const d = Math.hypot(dx, dy)
        if (d >= D) continue
        const a = (1 - d / D) ** 1.6 * EDGE * g * Math.min(p.q, o.q) * (1 + 1.2 * Math.max(p.hot, o.hot))
        const bi = Math.min(EDGE_BUCKETS - 1, Math.floor((a / (EDGE * 1.6)) * EDGE_BUCKETS))
        if (a < 0.006) continue
        buckets[bi].push(p.x, p.y, o.x, o.y)
      }
    }
    c.lineWidth = 1
    buckets.forEach((b, bi) => {
      if (!b.length) return
      c.strokeStyle = rgba(INK, ((bi + 0.5) / EDGE_BUCKETS) * EDGE * 1.6)
      c.beginPath()
      for (let k = 0; k < b.length; k += 4) {
        c.moveTo(b[k], b[k + 1])
        c.lineTo(b[k + 2], b[k + 3])
      }
      c.stroke()
    })

    // Field points.
    for (const n of field) {
      if (!onScreen(n.sx, n.sy, 4)) continue
      const a = (0.4 + 0.4 * n.hot) * g * n.q
      c.fillStyle = n.flare > 0 ? rgba(AMBER, a + 0.5 * n.flare * n.q) : rgba(INK, a)
      c.beginPath()
      c.arc(n.sx, n.sy, n.r + n.flare * 0.8, 0, 6.2832)
      c.fill()
      if (n.flare > 0.02) {
        c.strokeStyle = rgba(AMBER, 0.45 * n.flare * n.q * g)
        c.beginPath()
        c.arc(n.sx, n.sy, 3 + (1 - n.flare) * 12, 0, 6.2832)
        c.stroke()
      }
    }

    // Agents: wires into the hub, then the clusters on top.
    for (const a of agents) {
      if (!onScreen(a.x, a.y, 200)) continue
      const [hx, hy] = wire(a, 1)
      const grad = c.createLinearGradient(a.x, a.y, hx, hy)
      grad.addColorStop(0, rgba(a.rgb, 0.16 * g * a.q))
      grad.addColorStop(0.55, rgba(a.rgb, 0.06 * g))
      grad.addColorStop(1, rgba(a.rgb, 0))
      c.strokeStyle = grad
      c.beginPath()
      c.moveTo(a.x, a.y)
      c.bezierCurveTo(a.x + (hx - a.x) * 0.55, a.y, hx - (hx - a.x) * 0.45, hy, hx, hy)
      c.stroke()
    }
    for (const a of agents) {
      if (!onScreen(a.x, a.y, 60)) continue
      const q = a.q * g
      c.strokeStyle = rgba(a.rgb, 0.2 * q)
      c.beginPath()
      for (const s of a.sats) {
        c.moveTo(a.x, a.y)
        c.lineTo(s.x, s.y)
      }
      c.stroke()
      c.fillStyle = rgba(a.rgb, 0.42 * q)
      for (const s of a.sats) {
        c.beginPath()
        c.arc(s.x, s.y, 1.3, 0, 6.2832)
        c.fill()
      }
      c.fillStyle = rgba(a.rgb, 0.07 * q + 0.1 * a.flare * q)
      c.beginPath()
      c.arc(a.x, a.y, 9 + a.flare * 4, 0, 6.2832)
      c.fill()
      c.fillStyle = rgba(a.rgb, 0.62 * q)
      c.beginPath()
      c.arc(a.x, a.y, 2.3, 0, 6.2832)
      c.fill()
      if (a.flare > 0.02) {
        c.strokeStyle = rgba(AMBER, 0.5 * a.flare * q)
        c.beginPath()
        c.arc(a.x, a.y, 6 + (1 - a.flare) * 16, 0, 6.2832)
        c.stroke()
      }
    }

    if (mode === 'live') {
      drawPulses(c, g)
      drawLabels(c, g)
    }
  }

  function glowDot(c: CanvasRenderingContext2D, x: number, y: number, a: number) {
    c.fillStyle = rgba(AMBER, a * 0.16)
    c.beginPath()
    c.arc(x, y, 5, 0, 6.2832)
    c.fill()
    c.fillStyle = rgba(AMBER, a)
    c.beginPath()
    c.arc(x, y, 1.7, 0, 6.2832)
    c.fill()
  }

  function drawPulses(c: CanvasRenderingContext2D, g: number) {
    for (const p of fieldPulses) {
      const a = field[p.a]
      const b = field[p.b]
      if (!a || !b) continue
      const lerp = (u: number): [number, number] => [a.sx + (b.sx - a.sx) * u, a.sy + (b.sy - a.sy) * u]
      const [x, y] = lerp(p.t)
      const env = Math.min(1, p.t * 6, (1 - p.t) * 6 + 0.4)
      const q = quietAt(x, y)
      const [tx, ty] = lerp(Math.max(0, p.t - 0.3))
      const trail = c.createLinearGradient(tx, ty, x, y)
      trail.addColorStop(0, rgba(AMBER, 0))
      trail.addColorStop(1, rgba(AMBER, 0.5 * env * q * g))
      c.strokeStyle = trail
      c.beginPath()
      c.moveTo(tx, ty)
      c.lineTo(x, y)
      c.stroke()
      glowDot(c, x, y, 0.9 * env * q * g)
    }
    for (const p of wirePulses) {
      const a = agents[p.k]
      if (!onScreen(a.x, a.y, 200)) continue
      const u = p.out ? 1 - p.t : p.t
      // Fade out as it reaches the headline (inbound) and in as it leaves it (outbound).
      const env = Math.min(1, p.t * 5) * (1 - smooth((u - 0.55) / 0.35))
      if (env < 0.01) continue
      const [x, y] = wire(a, u)
      const [tx, ty] = wire(a, p.out ? Math.min(1, u + 0.08) : Math.max(0, u - 0.08))
      const trail = c.createLinearGradient(tx, ty, x, y)
      trail.addColorStop(0, rgba(AMBER, 0))
      trail.addColorStop(1, rgba(AMBER, 0.55 * env * g))
      c.strokeStyle = trail
      c.beginPath()
      c.moveTo(tx, ty)
      c.lineTo(x, y)
      c.stroke()
      glowDot(c, x, y, 0.9 * env * g * Math.max(0.35, quietAt(x, y)))
    }
  }

  function drawLabels(c: CanvasRenderingContext2D, g: number) {
    c.font = '400 11px "IBM Plex Mono", ui-monospace, monospace'
    c.textBaseline = 'middle'
    for (const l of labels) {
      const p = l.at()
      if (!p) continue
      const [x, y] = p
      const env = Math.min(1, l.age / 0.6, (l.life - l.age) / 0.9) * g * quietAt(x, y)
      if (env <= 0.01) continue
      const s = l.side
      c.strokeStyle = rgba(AMBER, 0.6 * env)
      c.beginPath()
      c.arc(x, y, 4.5, 0, 6.2832)
      c.moveTo(x + s * 3.2, y - 3.2)
      c.lineTo(x + s * 11, y - 11)
      c.lineTo(x + s * 17, y - 11)
      c.stroke()
      const kw = c.measureText(l.key).width
      const rw = c.measureText(l.rest).width
      const x0 = s > 0 ? x + 21 : x - 21 - kw - rw
      c.fillStyle = rgba(AMBER, 0.85 * env)
      c.fillText(l.key, x0, y - 11)
      if (l.rest) {
        c.fillStyle = rgba([169, 172, 167], 0.8 * env)
        c.fillText(l.rest, x0 + kw, y - 11)
      }
    }
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
    if (t >= nextMeasure) {
      nextMeasure = t + 1.5
      measureQuiet()
    }
    step(dt)
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

  let stillQueued = 0
  function still() {
    if (mode !== 'static' || stillQueued || stopped) return
    stillQueued = win.requestAnimationFrame(() => {
      stillQueued = 0
      measureQuiet()
      draw()
    })
  }

  let resizeTimer: ReturnType<typeof setTimeout> | undefined
  const onResize = () => {
    clearTimeout(resizeTimer)
    resizeTimer = setTimeout(() => {
      const w = Math.round(host.clientWidth || win.innerWidth)
      const h = Math.round(host.clientHeight || win.innerHeight)
      if (w === W && h === H) return
      resize()
      if (mode === 'static') draw()
    }, 200)
  }
  const onVisibility = () => {
    hidden = doc.visibilityState === 'hidden'
    update()
  }
  const onScroll = () => still()
  const onMotionPref = () => {
    mode = reduceMq.matches ? 'static' : 'live'
    fieldPulses.length = 0
    wirePulses.length = 0
    labels.length = 0
    update()
    if (mode === 'static') draw()
  }
  const onPointer = (e: PointerEvent) => {
    if (e.pointerType !== 'mouse') return
    if (!pointerOn && pointerAmt < 0.05) {
      cx = e.clientX
      cy = e.clientY
    }
    px = e.clientX
    py = e.clientY
    pointerOn = true
  }
  const onPointerOut = (e: PointerEvent) => {
    if (!e.relatedTarget) pointerOn = false
  }

  win.addEventListener('resize', onResize, { passive: true })
  win.addEventListener('scroll', onScroll, { passive: true })
  doc.addEventListener('visibilitychange', onVisibility)
  reduceMq.addEventListener?.('change', onMotionPref)
  if (finePointer) {
    win.addEventListener('pointermove', onPointer, { passive: true })
    win.addEventListener('pointerout', onPointerOut, { passive: true })
  }
  const io = win.IntersectionObserver
    ? new win.IntersectionObserver((entries) => {
        inView = entries[entries.length - 1].isIntersecting
        update()
      })
    : null
  io?.observe(host)

  resize()
  // Start with the scene already in motion rather than everything at rest.
  if (mode === 'live') {
    for (let i = 0; i < 20; i++) step(0.05)
    labels.length = 0
  }
  draw()
  host.classList.add('is-live')
  update()

  return {
    mode: () => mode,
    running: () => raf !== 0,
    frames: () => frames,
    points: () => field.length,
    stop() {
      stopped = true
      update()
      if (stillQueued) win.cancelAnimationFrame(stillQueued)
      clearTimeout(resizeTimer)
      win.removeEventListener('resize', onResize)
      win.removeEventListener('scroll', onScroll)
      win.removeEventListener('pointermove', onPointer)
      win.removeEventListener('pointerout', onPointerOut)
      doc.removeEventListener('visibilitychange', onVisibility)
      reduceMq.removeEventListener?.('change', onMotionPref)
      io?.disconnect()
      canvas.remove()
      host.classList.remove('is-live')
    },
  }
}

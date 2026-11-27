import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { AGENTS, DEMO_PATH, DOWNLOAD_URL, FAQ, FEATURES, LATEST_VERSION, MAC_DOWNLOAD_URL } from '../src/landing/content'
import { LandingPage } from '../src/landing/LandingPage'
import { LegalPage } from '../src/landing/legal'
import { detectOS } from '../src/landing/os'

const here = path.dirname(fileURLToPath(import.meta.url))
const frontend = path.resolve(here, '..')
const publicDir = path.join(frontend, 'public')
const read = (...p: string[]) => readFileSync(path.join(frontend, ...p), 'utf8')
const html = renderToStaticMarkup(createElement(LandingPage))
const count = (s: string, part: string) => s.split(part).length - 1

test('every screenshot the landing page references ships in public/', () => {
  const refs = new Set<string>()
  for (const m of html.matchAll(/\/landing\/shots\/[\w-]+\.(?:avif|webp|png)/g)) refs.add(m[0])
  assert.ok(refs.size > 0, 'no screenshots found in the page')
  for (const ref of refs) assert.ok(existsSync(path.join(publicDir, ref)), `missing ${ref}`)
})

test('screenshots are crisp captures never shown wider than half their pixel width', () => {
  const figs = [...html.matchAll(/--ratio:(\d+) \/ (\d+);--max:(\d+)px[\s\S]*?<img src="\/landing\/shots\/([\w-]+)-(\d+)\.png" width="(\d+)" height="(\d+)"/g)]
  assert.ok(figs.length >= 3 && figs.length <= 6, `expected a few shots, found ${figs.length}`)
  for (const [, rw, rh, max, name, file, w, h] of figs) {
    assert.ok(/^(web|desk)-/.test(name), `${name}: use the 2x captures`)
    assert.equal(file, w)
    assert.equal(Number(max), Math.round(Number(w) / 2), `${name}: shown wider than half its pixels`)
    assert.deepEqual([rw, rh], [w, h])
    // PNG header: width and height are big-endian at bytes 16 and 20.
    const png = readFileSync(path.join(publicDir, 'landing', 'shots', `${name}-${file}.png`))
    assert.deepEqual([png.readUInt32BE(16), png.readUInt32BE(20)], [Number(w), Number(h)], `${name}: size attributes are wrong`)
    assert.ok(Number(w) >= 1170, `${name}: capture is too small to be sharp`)
    for (const ext of ['avif', 'webp']) {
      assert.ok(existsSync(path.join(publicDir, 'landing', 'shots', `${name}-${w}.${ext}`)))
      assert.ok(existsSync(path.join(publicDir, 'landing', 'shots', `${name}-${Math.round(Number(w) / 2)}.${ext}`)))
    }
  }
})

test('every image has alt text', () => {
  for (const m of html.matchAll(/<img [^>]*>/g)) assert.match(m[0], / alt="/, `image without alt: ${m[0]}`)
})

test('one short page: hero with the pipeline, feature tabs, film and the rest, FAQ, download', () => {
  const ids = [...html.matchAll(/<section id="([\w-]+)"/g)].map((m) => m[1])
  assert.deepEqual(ids, ['how', 'features', 'more', 'faq', 'download'])
  assert.match(html, /<h1 id="hero-title">Every coding agent in one app, with one shared memory\.<\/h1>/)
  const lede = html.match(/<p class="nx-hero-lede">([^<]*)<\/p>/)?.[1] ?? ''
  const words = lede.trim().split(/\s+/).length
  assert.ok(words >= 15 && words <= 28, `hero lede is ${words} words`)
  assert.ok(FAQ.length <= 7, `FAQ has ${FAQ.length} questions`)
  // In-page links point at sections that exist.
  for (const m of html.matchAll(/<a [^>]*href="#([\w-]+)"/g)) assert.match(html, new RegExp(`id="${m[1]}"`), `#${m[1]} has no target`)
})

test('the pipeline names every agent once and explains each stage in words', () => {
  const flow = html.match(/<figure class="nx-flow"[\s\S]*?<\/figure>/)?.[0] ?? ''
  assert.ok(flow, 'pipeline is missing')
  for (const a of AGENTS) assert.equal(flow.split(`data-agent="${a.id}"`).length - 1, 1, `${a.name} in the pipeline`)
  for (const t of ['Captures every chat', 'Removes secrets', 'Keeps one memory', 'Your next chat starts with context', 'Ask why a line changed', 'Follow along anywhere', 'Hand off to a teammate']) {
    assert.ok(flow.includes(t), `pipeline stage missing: ${t}`)
  }
  assert.match(flow, /<figcaption id="flow-title" class="nx-sr">How Nexus works/)
  // The wires are decoration, hidden from assistive tech.
  for (const m of flow.matchAll(/<svg class="nx-wires[^>]*>/g)) assert.match(m[0], /aria-hidden="true"/)
  const css = read('src', 'landing', 'site.css')
  assert.match(css, /@media \(prefers-reduced-motion: no-preference\) \{\s*\.nx-wire-flow \{ animation/, 'wire animation must be opt-in')
})

test('feature tabs are an accessible tablist with one panel per tab', () => {
  const tabs = [...html.matchAll(/<button type="button" role="tab" id="tab-([\w-]+)"[^>]*aria-selected="(true|false)" aria-controls="panel-([\w-]+)"/g)]
  assert.deepEqual(tabs.map((t) => t[1]), ['chat', 'control', 'memory', 'live'])
  assert.equal(tabs.filter((t) => t[2] === 'true').length, 1)
  for (const [, id, , panel] of tabs) {
    assert.equal(id, panel)
    assert.match(html, new RegExp(`id="panel-${id}" role="tabpanel" aria-labelledby="tab-${id}"`))
  }
  assert.match(html, /role="tablist" aria-label="Features"/)
  assert.match(read('src', 'landing', 'client.ts'), /ArrowRight[\s\S]*ArrowLeft[\s\S]*Home[\s\S]*End/)
  // Panels only share a cell (and hide) once the head script marks JS as on.
  assert.match(read('index.html'), /classList\.add\('nx-js'\)/)
})

test('downloads follow the visitor: Windows by default, Mac, and the demo first on phones and other systems', () => {
  const cta = html.match(/<div class="nx-cta">[\s\S]*?<\/div>/)?.[0] ?? ''
  assert.match(cta, new RegExp(`class="nx-btn nx-btn-primary nx-cta-dl" data-for="win" href="${DOWNLOAD_URL}"`))
  assert.match(cta, new RegExp(`class="nx-btn nx-btn-primary nx-cta-dl" data-for="mac" href="${MAC_DOWNLOAD_URL}"`))
  assert.match(cta, new RegExp(`class="nx-btn nx-btn-primary nx-cta-dl" data-for="mobile other" href="${DEMO_PATH}"`))
  assert.match(cta, /data-for="win mac" href="\/demo"/)
  // Each OS sees exactly one primary button in a CTA group.
  for (const os of ['win', 'mac', 'mobile', 'other']) {
    const primaries = [...cta.matchAll(/nx-btn-primary[^"]*" data-for="([^"]+)"/g)].filter((m) => m[1].split(' ').includes(os))
    assert.equal(primaries.length, 1, `${os} sees ${primaries.length} primary buttons`)
  }
  assert.equal(count(html, '<div class="nx-cta">'), 2, 'hero and final CTA')
  const css = read('src', 'landing', 'site.css')
  // No JS: the Windows set shows.
  assert.ok(css.includes("html:not([data-os]) .nx [data-for]:not([data-for~='win'])"))
  for (const os of ['win', 'mac', 'mobile', 'other']) assert.ok(css.includes(`html[data-os='${os}'] .nx [data-for]:not([data-for~='${os}'])`), `css for ${os}`)
  // Swapping must not shift the layout: the primary buttons share one width.
  assert.match(css, /\.nx-cta \.nx-btn \{ min-width: \d+px; \}/)
  assert.match(read('src', 'landing', 'client.ts'), /dataset\.os = detectOS\(/)
})

test('detectOS picks the right download for each kind of device', () => {
  const ua = {
    win: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36',
    mac: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15',
    iphone: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1',
    android: 'Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36',
    linux: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36',
  }
  assert.equal(detectOS({ userAgent: ua.win, platform: 'Win32' }), 'win')
  assert.equal(detectOS({ userAgent: ua.win, userAgentData: { platform: 'Windows', mobile: false } }), 'win')
  assert.equal(detectOS({ userAgent: ua.mac, platform: 'MacIntel', maxTouchPoints: 0 }), 'mac')
  assert.equal(detectOS({ userAgent: ua.mac, userAgentData: { platform: 'macOS', mobile: false } }), 'mac')
  // iPadOS reports a Mac; the touch screen gives it away.
  assert.equal(detectOS({ userAgent: ua.mac, platform: 'MacIntel', maxTouchPoints: 5 }), 'mobile')
  assert.equal(detectOS({ userAgent: ua.iphone, platform: 'iPhone', maxTouchPoints: 5 }), 'mobile')
  assert.equal(detectOS({ userAgent: ua.android, userAgentData: { platform: 'Android', mobile: true } }), 'mobile')
  assert.equal(detectOS({ userAgent: ua.linux, platform: 'Linux x86_64' }), 'other')
  assert.equal(detectOS({ userAgent: ua.linux, userAgentData: { platform: 'Chrome OS', mobile: false } }), 'other')
})

test('only the hero carries the "New in" note; no per-section badges', () => {
  assert.equal(count(html, `New in ${LATEST_VERSION}`), 1)
  assert.ok(!html.includes('nx-new-chip'))
  assert.ok(!html.includes('Three chats at once'), 'stale hero caption')
})

test('the landing and legal pages are dark only: no theme toggle, no light tokens', () => {
  assert.ok(!html.includes('data-theme-toggle'))
  assert.ok(!renderToStaticMarkup(createElement(LegalPage, { kind: 'privacy' })).includes('data-theme-toggle'))
  const css = read('src', 'landing', 'site.css')
  assert.ok(!/prefers-color-scheme|data-theme/.test(css), 'site.css still switches themes')
  assert.match(css, /html\.nx-home \{\s*color-scheme: dark;/)
  assert.ok(!read('src', 'landing', 'client.ts').includes('nx-theme'))
  assert.ok(!read('index.html').includes("'nx-theme'"), 'index.html still restores a landing theme')
  assert.ok(!read('scripts', 'prerender.tsx').includes("'nx-theme'"), 'legal pages still restore a theme')
})

test('downloads, the macOS beta, the live demo and the app logo stay linked', () => {
  assert.ok(html.includes(`href="${DOWNLOAD_URL}"`))
  assert.ok(html.includes(`href="${MAC_DOWNLOAD_URL}"`))
  assert.ok(html.includes(`href="${DEMO_PATH}"`))
  assert.match(html, /id="mac"/)
  assert.ok(html.includes('src="/brand/nexus-app.svg"'))
})

test('the page only names the agents the app supports', () => {
  const names = AGENTS.map((a) => a.name)
  for (const name of ['Windsurf', 'Copilot', 'Antigravity', 'Aider']) {
    assert.ok(!names.includes(name as never), `${name} is listed as supported`)
    assert.ok(!html.includes(name), `the page mentions ${name}`)
  }
})

test('structured data describes the features and the FAQ in plain text', () => {
  assert.ok(FEATURES.length >= 10)
  for (const f of FEATURES) assert.ok(f.length < 120 && !/[<>]/.test(f), `feature text: ${f}`)
  for (const f of FAQ) assert.ok(!/[<>]/.test(f.a), `FAQ answer has markup: ${f.q}`)
  const prerender = read('scripts', 'prerender.tsx')
  assert.match(prerender, /featureList: FEATURES/)
  assert.match(prerender, /softwareVersion: LATEST_VERSION/)
  assert.equal(LATEST_VERSION, '0.4.5')
})

test('the film poster is a sharp 1920px frame served at 1x and 2x, under a scrim and a play badge', () => {
  const film = html.match(/<figure class="nx-film"[\s\S]*?<\/figure>/)?.[0] ?? ''
  assert.match(film, /nexus-film-poster-960\.avif 960w, \/landing\/nexus-film-poster-1920\.avif 1920w/)
  assert.match(film, /<img class="nx-film-poster" src="\/landing\/nexus-film-poster-1920\.jpg" alt="" width="1920" height="1080"/)
  for (const f of ['nexus-film-poster-1920.jpg', 'nexus-film-poster-1920.avif', 'nexus-film-poster-960.webp']) assert.ok(existsSync(path.join(publicDir, 'landing', f)), f)
  assert.match(film, /class="nx-film-badge"/)
  // Never shown wider than half the frame's pixels.
  assert.match(read('src', 'landing', 'site.css'), /\.nx-film \{\s*position: relative; width: 100%; max-width: 960px;/)
})

test('the Control tab shows the desktop review panel with its own caption', () => {
  const panel = html.match(/id="panel-control"[\s\S]*?<\/figure>/)?.[0] ?? ''
  assert.match(panel, /shots\/desk-review-1456\.png/)
  assert.match(panel, /Nexus for Windows <span aria-hidden="true">·<\/span> Review changes/)
})

/* ---------- ambient background (src/landing/ambient.ts) ---------- */

type Listener = (e: unknown) => void
function fakeTarget() {
  const on = new Map<string, Set<Listener>>()
  return {
    addEventListener: (t: string, f: Listener) => {
      if (!on.has(t)) on.set(t, new Set())
      on.get(t)!.add(f)
    },
    removeEventListener: (t: string, f: Listener) => void on.get(t)?.delete(f),
    emit: (t: string, e: unknown = {}) => on.get(t)?.forEach((f) => f(e)),
    count: (t: string) => on.get(t)?.size ?? 0,
  }
}

/** Just enough of a browser for ambient.ts: a 2D context that draws nothing, a frame queue, an observer. */
function fakeBrowser({ reduce = false, cores = 8, memory = 8, width = 1440, height = 900 } = {}) {
  const frames = new Map<number, (now: number) => void>()
  let id = 0
  let now = 0
  let io: { cb: (e: { isIntersecting: boolean }[]) => void; observed: unknown[] } | null = null
  const ctx = new Proxy({} as Record<string | symbol, unknown>, {
    get: (o, k) =>
      k in o ? o[k]
      : k === 'measureText' ? () => ({ width: 80 })
      : k === 'createLinearGradient' || k === 'createRadialGradient' ? () => ({ addColorStop() {} })
      : () => undefined,
    set: (o, k, v) => {
      o[k] = v
      return true
    },
  })
  const canvas = {
    attrs: {} as Record<string, string>,
    className: '',
    width: 0,
    height: 0,
    removed: false,
    setAttribute(k: string, v: string) {
      this.attrs[k] = v
    },
    getContext: () => ctx,
    remove() {
      this.removed = true
    },
  }
  const classes = new Set<string>()
  const host = {
    clientWidth: width,
    clientHeight: height,
    children: [] as unknown[],
    appendChild(c: unknown) {
      this.children.push(c)
    },
    classList: { add: (c: string) => void classes.add(c), remove: (c: string) => void classes.delete(c) },
  }
  const heading = { getBoundingClientRect: () => ({ left: 370, top: 160, right: 1070, bottom: 420, width: 700, height: 260 }) }
  const doc = {
    ...fakeTarget(),
    visibilityState: 'visible',
    createElement: () => canvas,
    querySelector: (s: string) => (s === '.nx-hero-copy h1' ? heading : null),
    querySelectorAll: () => [heading],
  }
  const win = {
    ...fakeTarget(),
    innerWidth: width,
    innerHeight: height,
    scrollY: 0,
    devicePixelRatio: 3,
    navigator: { hardwareConcurrency: cores, deviceMemory: memory },
    matchMedia: (q: string) => ({ matches: q.includes('reduce') ? reduce : q.includes('fine'), ...fakeTarget() }),
    requestAnimationFrame: (f: (now: number) => void) => {
      frames.set(++id, f)
      return id
    },
    cancelAnimationFrame: (i: number) => void frames.delete(i),
    IntersectionObserver: class {
      constructor(cb: (e: { isIntersecting: boolean }[]) => void) {
        io = { cb, observed: [] }
      }
      observe(el: unknown) {
        io!.observed.push(el)
      }
      disconnect() {}
    },
  }
  /** Run animation frames for `ms` of wall time at 60 Hz. */
  const run = (ms: number) => {
    for (const end = now + ms; now < end; ) {
      now += 1000 / 60
      const due = [...frames.values()]
      frames.clear()
      for (const f of due) f(now)
    }
  }
  return { win, doc, host, canvas, classes, run, pending: () => frames.size, intersect: (v: boolean) => io!.cb([{ isIntersecting: v }]), io: () => io }
}

async function ambientIn(b: ReturnType<typeof fakeBrowser>) {
  const { startAmbient } = await import('../src/landing/ambient')
  return startAmbient(b.host as unknown as HTMLElement, { win: b.win as never, doc: b.doc as never, seed: 7 })
}

test('the ambient layer is prerendered as decoration only: aria-hidden, no pointer events, fixed behind the page', () => {
  assert.match(html, /<div class="nx"><div class="nx-ambient" aria-hidden="true"><\/div>/)
  const css = read('src', 'landing', 'site.css')
  const rule = css.match(/\.nx-ambient \{[^}]*\}/)?.[0] ?? ''
  assert.match(rule, /position: fixed;/)
  assert.match(rule, /z-index: -1;/)
  assert.match(rule, /pointer-events: none;/)
  // No-JS fallback: a still CSS lattice the canvas cross-fades over.
  assert.match(css, /\.nx-ambient::before \{/)
  assert.match(css, /\.nx-ambient\.is-live::before \{ opacity: 0; \}/)
  // Loaded lazily, after the page has loaded, as its own chunk.
  const client = read('src', 'landing', 'client.ts')
  assert.match(client, /import\('\.\/ambient'\)/)
  assert.ok(!/^import .*ambient/m.test(client), 'ambient.ts must not be in the first chunk')
})

test('ambient: the canvas it adds is aria-hidden, sized for the screen and capped at 2x', async () => {
  const b = fakeBrowser()
  const amb = await ambientIn(b)
  assert.deepEqual(b.host.children, [b.canvas])
  assert.equal(b.canvas.attrs['aria-hidden'], 'true')
  assert.equal(b.canvas.width, 1440 * 2)
  assert.equal(b.canvas.height, 900 * 2)
  assert.ok(b.classes.has('is-live'))
  assert.equal(amb.mode(), 'live')
  amb.stop()
  assert.ok(b.canvas.removed)
  assert.equal(b.win.count('resize'), 0)
  assert.equal(b.doc.count('visibilitychange'), 0)
})

test('ambient: with reduced motion it draws one still frame and never animates', async () => {
  const b = fakeBrowser({ reduce: true })
  const amb = await ambientIn(b)
  assert.equal(amb.mode(), 'static')
  assert.equal(amb.frames(), 1)
  assert.equal(amb.running(), false)
  assert.equal(b.pending(), 0, 'no animation frame requested')
  b.run(2000)
  assert.equal(amb.frames(), 1)
  amb.stop()
})

test('ambient: about 30 fps, paused while the tab is hidden or the layer is off screen', async () => {
  const b = fakeBrowser()
  const amb = await ambientIn(b)
  assert.ok(amb.running())
  let f0 = amb.frames()
  b.run(1000)
  const fps = amb.frames() - f0
  assert.ok(fps >= 28 && fps <= 31, `${fps} frames in a second`)

  b.doc.visibilityState = 'hidden'
  b.doc.emit('visibilitychange')
  assert.equal(amb.running(), false)
  f0 = amb.frames()
  b.run(1000)
  assert.equal(amb.frames(), f0, 'drew while the tab was hidden')
  b.doc.visibilityState = 'visible'
  b.doc.emit('visibilitychange')
  assert.ok(amb.running())

  assert.deepEqual(b.io()!.observed, [b.host])
  b.intersect(false)
  assert.equal(amb.running(), false)
  f0 = amb.frames()
  b.run(500)
  assert.equal(amb.frames(), f0, 'drew while off screen')
  b.intersect(true)
  assert.ok(amb.running())
  b.run(20_000) // a long stretch with pulses, hops and notes must not throw
  amb.stop()
  assert.equal(amb.running(), false)
})

test('ambient: fewer points on phones and low-power devices', async () => {
  const { pointBudget, isLowPower } = await import('../src/landing/ambient')
  assert.ok(isLowPower({ hardwareConcurrency: 4 }))
  assert.ok(isLowPower({ hardwareConcurrency: 8, deviceMemory: 4 }))
  assert.ok(!isLowPower({ hardwareConcurrency: 8, deviceMemory: 8 }))
  const desk = pointBudget(1440, 900, false)
  assert.ok(pointBudget(375, 812, false) < desk / 2)
  assert.ok(pointBudget(1440, 900, true) < desk)
  const b = fakeBrowser({ cores: 4, width: 375, height: 812 })
  const amb = await ambientIn(b)
  assert.equal(amb.points(), pointBudget(375, 812, true))
  assert.equal(b.canvas.width, Math.round(375 * 1.5), 'low-power devices draw at 1.5x at most')
  amb.stop()
})

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

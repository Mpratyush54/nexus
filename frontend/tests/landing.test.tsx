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

test('a few large screenshots, each with width and height that match its file', () => {
  const imgs = [...html.matchAll(/<img src="\/landing\/shots\/([\w-]+)-(\d+)\.png" width="(\d+)" height="(\d+)"/g)]
  assert.ok(imgs.length >= 3 && imgs.length <= 6, `expected a few large shots, found ${imgs.length}`)
  for (const [, name, file, w, h] of imgs) {
    assert.equal(file, w, `${name}: file width and width attribute differ`)
    // PNG header: width and height are big-endian at bytes 16 and 20.
    const png = readFileSync(path.join(publicDir, 'landing', 'shots', `${name}-${file}.png`))
    assert.deepEqual([png.readUInt32BE(16), png.readUInt32BE(20)], [Number(w), Number(h)], `${name}: size attributes are wrong`)
  }
})

test('every image has alt text, and only the hero shot loads eagerly', () => {
  for (const m of html.matchAll(/<img [^>]*>/g)) assert.match(m[0], / alt="/, `image without alt: ${m[0]}`)
  const eager = [...html.matchAll(/<img [^>]*loading="eager"[^>]*>/g)]
  assert.equal(eager.length, 1)
  assert.match(eager[0][0], /shots\/running-/)
})

test('one story in a short page: hero, three pillars, film, the rest, FAQ and download', () => {
  const ids = [...html.matchAll(/<section(?: id="([\w-]+)")?/g)].map((m) => m[1] ?? 'hero')
  assert.deepEqual(ids, ['hero', 'chat', 'control', 'memory', 'film', 'more', 'faq', 'download'])
  assert.match(html, /<h1 id="hero-title">Every coding agent in one app, with one shared memory\.<\/h1>/)
  const lede = html.match(/<p class="nx-hero-lede">([^<]*)<\/p>/)?.[1] ?? ''
  const words = lede.trim().split(/\s+/).length
  assert.ok(words >= 15 && words <= 28, `hero lede is ${words} words`)
  // Agents are named once, in the strip under the hero.
  assert.equal(count(html, 'class="nx-agents"'), 1)
  for (const a of AGENTS) assert.ok(html.includes(`data-agent="${a.id}"><span class="nx-agent-dot"`), `${a.name} missing from the strip`)
  // In-page links point at sections that exist.
  for (const m of html.matchAll(/<a [^>]*href="#([\w-]+)"/g)) assert.match(html, new RegExp(`id="${m[1]}"`), `#${m[1]} has no target`)
})

test('only the hero carries the "New in" note; no per-section badges', () => {
  assert.equal(count(html, `New in ${LATEST_VERSION}`), 1)
  assert.ok(!html.includes('nx-new-chip'))
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

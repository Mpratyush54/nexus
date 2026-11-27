import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { AGENTS, FAQ, FEATURES, LATEST_VERSION } from '../src/landing/content'
import { LandingPage } from '../src/landing/LandingPage'

const here = path.dirname(fileURLToPath(import.meta.url))
const publicDir = path.resolve(here, '..', 'public')
const html = renderToStaticMarkup(createElement(LandingPage))

test('every screenshot the landing page references ships in public/', () => {
  const refs = new Set<string>()
  for (const m of html.matchAll(/\/landing\/shots\/[\w-]+\.(?:avif|webp|png)/g)) refs.add(m[0])
  assert.ok(refs.size > 0, 'no screenshots found in the page')
  for (const ref of refs) assert.ok(existsSync(path.join(publicDir, ref)), `missing ${ref}`)
})

test('every screenshot is a 5:3 frame with width and height set', () => {
  for (const m of html.matchAll(/<img src="\/landing\/shots\/[^"]+" width="(\d+)" height="(\d+)"/g)) {
    const [w, h] = [Number(m[1]), Number(m[2])]
    assert.ok(Math.abs(w / h - 5 / 3) < 0.01, `shot is ${w}x${h}, not 5:3`)
  }
})

test('the new agent features each have a section the nav and hero can link to', () => {
  for (const id of ['work', 'control', 'undo', 'composer', 'anywhere', 'install']) {
    assert.match(html, new RegExp(`<section id="${id}"`), `section #${id} is missing`)
  }
  assert.match(html, /href="#work"/)
  assert.ok(html.includes(`New in ${LATEST_VERSION}`), 'hero "New in" note is missing')
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
  const prerender = readFileSync(path.resolve(here, '..', 'scripts', 'prerender.tsx'), 'utf8')
  assert.match(prerender, /featureList: FEATURES/)
})

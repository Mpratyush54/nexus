import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const repo = path.resolve(here, '..', '..')

/** Static paths the router serves, e.g. "login", "app/chats" (no params, no splats). */
function routerPaths(): string[] {
  const src = readFileSync(path.join(repo, 'frontend', 'src', 'App.tsx'), 'utf8')
  const out = new Set<string>()
  for (const m of src.matchAll(/path="([^"]+)"/g)) {
    const p = m[1]
    if (p === '/' || p.includes('*') || p.includes(':')) continue
    // Nested routes under <Route path="/app"> are relative.
    out.add(p.startsWith('/') ? p.slice(1) : `app/${p}`)
  }
  // /demo is booted by main.tsx (src/demo/boot.tsx) rather than declared in App.tsx.
  const main = readFileSync(path.join(repo, 'frontend', 'src', 'main.tsx'), 'utf8')
  if (main.includes("path === '/demo'")) out.add('demo')
  return [...out].sort()
}

function deployedPaths(): string[] {
  const sh = readFileSync(path.join(repo, 'deploy', 'deploy-frontend.sh'), 'utf8')
  const block = sh.match(/SPA_ROUTES=\(([\s\S]*?)\)/)
  assert.ok(block, 'SPA_ROUTES array not found in deploy-frontend.sh')
  return block[1].split(/\s+/).filter(Boolean).sort()
}

test('every static client route is uploaded as its own S3 object, so it answers 200', () => {
  assert.deepEqual(deployedPaths(), routerPaths())
})

test('the static legal pages are uploaded under clean URLs, listed in the sitemap and not SPA routes', () => {
  const sh = readFileSync(path.join(repo, 'deploy', 'deploy-frontend.sh'), 'utf8')
  const block = sh.match(/STATIC_PAGES=\(([^)]*)\)/)
  assert.ok(block, 'STATIC_PAGES array not found in deploy-frontend.sh')
  const pages = block[1].split(/\s+/).filter(Boolean).sort()
  assert.deepEqual(pages, ['privacy', 'terms'])
  const sitemap = readFileSync(path.join(repo, 'frontend', 'public', 'sitemap.xml'), 'utf8')
  for (const p of pages) {
    assert.ok(sitemap.includes(`/${p}</loc>`), `${p} missing from sitemap.xml`)
    assert.ok(!deployedPaths().includes(p), `${p} must not be served the SPA shell`)
  }
})

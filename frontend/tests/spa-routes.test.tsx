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

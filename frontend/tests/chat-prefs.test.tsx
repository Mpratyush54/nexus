import { test } from 'node:test'
import assert from 'node:assert/strict'
import { chatPrefsFromCards, groupChats, legacyPatches, migrateLocalChatPrefs } from '../src/lib/chat-prefs'
import { ApiError } from '../src/types/api'

function memoryStore(init: Record<string, string> = {}) {
  const map = new Map(Object.entries(init))
  return {
    map,
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => void map.set(k, v),
    removeItem: (k: string) => void map.delete(k),
  }
}

test('pins, archives and renames come from the server cards', () => {
  const cards = [
    { session_id: 'a', title: 'Fix the build', renamed: true },
    { session_id: 'b', archived: true },
    { session_id: 'c', pinned: true },
    { session_id: 'd' },
  ]
  const prefs = chatPrefsFromCards(cards, new Map())
  assert.deepEqual(prefs, { pinned: ['c'], archived: ['b'], titles: { a: 'Fix the build' } })
  const g = groupChats(cards, prefs)
  assert.deepEqual(g.pinned.map((c) => c.session_id), ['c'])
  assert.deepEqual(g.recent.map((c) => c.session_id), ['a', 'd'])
  assert.deepEqual(g.archived.map((c) => c.session_id), ['b'])
})

test('a change waiting for the server shows over the cards', () => {
  const cards = [{ session_id: 'a', pinned: true }, { session_id: 'b' }, { session_id: 'c', title: 'Old', renamed: true }]
  const overlay = new Map([
    ['a', { archived: true, at: 1 }],
    ['b', { pinned: true, at: 2 }],
    ['c', { title: '', at: 3 }],
  ])
  assert.deepEqual(chatPrefsFromCards(cards, overlay), { pinned: ['b'], archived: ['a'], titles: {} })
})

test('the old browser-only state becomes one patch per chat, pin winning over archive', () => {
  const patches = legacyPatches({ pinned: ['a'], archived: ['a', 'b'], titles: { b: ' Renamed ' } })
  assert.deepEqual(Object.fromEntries(patches), { a: { pinned: true }, b: { archived: true, title: 'Renamed' } })
})

test('the old local state moves to the server once; failures stay for the next visit', async () => {
  const key = 'nexus.chat-prefs.u1'
  const store = memoryStore({ [key]: JSON.stringify({ pinned: ['a', 'gone'], archived: ['b'], titles: { c: 'Mine' } }) })
  const sent: [string, unknown][] = []
  const moved = await migrateLocalChatPrefs('u1', store, async (id, patch) => {
    sent.push([id, patch])
    if (id === 'gone') throw new ApiError(404, 'not found')
    if (id === 'b') throw new Error('offline')
  })
  assert.equal(moved, 2)
  assert.deepEqual(sent.map(([id]) => id).sort(), ['a', 'b', 'c', 'gone'])
  assert.deepEqual(JSON.parse(store.getItem(key)!), { pinned: [], archived: ['b'], titles: {} })
  // Once per visit: a second call does not resend.
  assert.equal(await migrateLocalChatPrefs('u1', store, async () => assert.fail('sent twice')), 2)
})

test('with nothing kept locally, nothing is sent', async () => {
  const store = memoryStore()
  assert.equal(await migrateLocalChatPrefs('u2', store, async () => assert.fail('sent')), 0)
})

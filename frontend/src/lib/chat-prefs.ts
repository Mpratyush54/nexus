import { useSyncExternalStore } from 'react'

/**
 * Pinned, archived and renamed chats. The server has no pin, archive or
 * rename for synced chats (a rename would be overwritten by the next capture
 * from the desktop), so these are kept per person in this browser.
 */
export type ChatPrefs = {
  pinned: string[]
  archived: string[]
  titles: Record<string, string>
}

const EMPTY: ChatPrefs = { pinned: [], archived: [], titles: {} }
const MAX_PINNED = 10
const key = (userId: string) => `nexus.chat-prefs.${userId || 'anon'}`

const cache = new Map<string, ChatPrefs>()
const listeners = new Set<() => void>()

function load(userId: string): ChatPrefs {
  const hit = cache.get(userId)
  if (hit) return hit
  let prefs = EMPTY
  try {
    const raw = localStorage.getItem(key(userId))
    if (raw) {
      const v = JSON.parse(raw) as Partial<ChatPrefs>
      prefs = {
        pinned: Array.isArray(v.pinned) ? v.pinned.filter((x) => typeof x === 'string') : [],
        archived: Array.isArray(v.archived) ? v.archived.filter((x) => typeof x === 'string') : [],
        titles: v.titles && typeof v.titles === 'object' ? v.titles : {},
      }
    }
  } catch {
    /* unreadable or blocked storage: start empty */
  }
  cache.set(userId, prefs)
  return prefs
}

function save(userId: string, next: ChatPrefs) {
  cache.set(userId, next)
  try {
    localStorage.setItem(key(userId), JSON.stringify(next))
  } catch {
    /* kept for this visit */
  }
  for (const l of listeners) l()
}

export function useChatPrefs(userId: string): ChatPrefs {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => load(userId),
    () => EMPTY,
  )
}

const without = (list: string[], id: string) => list.filter((x) => x !== id)

export function togglePin(userId: string, chatId: string): boolean {
  const p = load(userId)
  const pinned = p.pinned.includes(chatId)
  save(userId, {
    ...p,
    pinned: pinned ? without(p.pinned, chatId) : [chatId, ...p.pinned].slice(0, MAX_PINNED),
    archived: without(p.archived, chatId),
  })
  return !pinned
}

export function setArchived(userId: string, chatId: string, archived: boolean) {
  const p = load(userId)
  save(userId, {
    ...p,
    archived: archived ? [chatId, ...without(p.archived, chatId)] : without(p.archived, chatId),
    pinned: archived ? without(p.pinned, chatId) : p.pinned,
  })
}

export function renameChat(userId: string, chatId: string, title: string) {
  const p = load(userId)
  const titles = { ...p.titles }
  if (title.trim()) titles[chatId] = title.trim().slice(0, 120)
  else delete titles[chatId]
  save(userId, { ...p, titles })
}

export function forgetChat(userId: string, chatId: string) {
  const p = load(userId)
  const titles = { ...p.titles }
  delete titles[chatId]
  save(userId, { pinned: without(p.pinned, chatId), archived: without(p.archived, chatId), titles })
}

/** Splits loaded chats into pinned (in pin order), the rest, and archived. */
export function groupChats<T extends { session_id: string }>(cards: T[], prefs: ChatPrefs) {
  const byId = new Map(cards.map((c) => [c.session_id, c]))
  const pinned = prefs.pinned.map((id) => byId.get(id)).filter((c): c is T => !!c)
  const archivedSet = new Set(prefs.archived)
  const pinnedSet = new Set(prefs.pinned)
  return {
    pinned,
    recent: cards.filter((c) => !pinnedSet.has(c.session_id) && !archivedSet.has(c.session_id)),
    archived: cards.filter((c) => archivedSet.has(c.session_id) && !pinnedSet.has(c.session_id)),
  }
}

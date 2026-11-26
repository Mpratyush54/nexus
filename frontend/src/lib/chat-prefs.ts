import { useMemo, useSyncExternalStore } from 'react'
import { ApiError } from '@/types/api'

/**
 * Pinned, archived and renamed chats. They are the person's own and live on
 * the server (PATCH /v1/agent-sessions/{id}/state; /v1/timeline marks items
 * pinned, archived and renamed and shows the renamed title), so the web and
 * the desktop apps agree. A change shows at once and is taken back if the
 * server refuses it.
 *
 * Earlier web builds kept these in this browser's localStorage; the first
 * signed-in visit moves them to the server once (migrateLocalChatPrefs).
 */
export type ChatPrefs = {
  pinned: string[]
  archived: string[]
  titles: Record<string, string>
}

/** What a timeline card carries about the caller's own state. */
export type ChatStateCard = {
  session_id: string
  title?: string
  pinned?: boolean
  archived?: boolean
  renamed?: boolean
}

export type ChatStatePatch = { title?: string; pinned?: boolean; archived?: boolean }

export const MAX_PINNED = 10
const EMPTY: ChatPrefs = { pinned: [], archived: [], titles: {} }
const legacyKey = (userId: string) => `nexus.chat-prefs.${userId || 'anon'}`

// Changes sent but not yet seen in a reloaded list.
type Pending = { pinned?: boolean; archived?: boolean; title?: string; at: number }
const pending = new Map<string, Pending>()
let version = 0
const listeners = new Set<() => void>()
let refresher: (() => Promise<unknown>) | null = null

function changed() {
  version++
  for (const l of listeners) l()
}

/** The chat list registers how to reload itself after a change is saved. */
export function setChatListRefresher(fn: (() => Promise<unknown>) | null) {
  refresher = fn
}

function useVersion() {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => version,
    () => 0,
  )
}

/** Builds ChatPrefs from the server's cards, with unsaved changes on top. */
export function chatPrefsFromCards(cards: ChatStateCard[], overlay: ReadonlyMap<string, Pending> = pending): ChatPrefs {
  const pinned: string[] = []
  const archived: string[] = []
  const titles: Record<string, string> = {}
  const seen = new Set<string>()
  for (const c of cards) {
    if (seen.has(c.session_id)) continue
    seen.add(c.session_id)
    const p = overlay.get(c.session_id)
    const isPinned = p?.pinned ?? (p?.archived ? false : !!c.pinned)
    const isArchived = p?.archived ?? (p?.pinned ? false : !!c.archived)
    if (isPinned) pinned.push(c.session_id)
    else if (isArchived) archived.push(c.session_id)
    if (p?.title !== undefined) {
      if (p.title) titles[c.session_id] = p.title
    } else if (c.renamed && c.title) {
      titles[c.session_id] = c.title
    }
  }
  // A chat pinned just now goes first; the rest keep the list's order.
  const pinnedAt = (id: string) => (overlay.get(id)?.pinned ? overlay.get(id)!.at : 0)
  pinned.sort((a, b) => pinnedAt(b) - pinnedAt(a))
  return { pinned, archived, titles }
}

/** The caller's chat state over the cards loaded so far. */
export function useChatPrefs(cards: ChatStateCard[] | undefined): ChatPrefs {
  const v = useVersion()
  return useMemo(
    () => (cards?.length ? chatPrefsFromCards(cards) : EMPTY),
    // `v` changes when a pending change starts or ends.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [cards, v],
  )
}

async function sendState(chatId: string, patch: ChatStatePatch) {
  // Loaded on first use, so the pure helpers here stay importable in tests.
  const { apiRequest } = await import('@/lib/api-client')
  return apiRequest<{ session_id: string; title?: string; pinned?: boolean; archived?: boolean }>(
    `/v1/agent-sessions/${encodeURIComponent(chatId)}/state`,
    { method: 'PATCH', body: patch },
  )
}

/**
 * Shows `patch` at once and saves it. Resolves when it is saved and the list
 * has reloaded; on failure the change is taken back and the promise rejects.
 */
export async function updateChatState(chatId: string, patch: ChatStatePatch): Promise<void> {
  const before = pending.get(chatId)
  const next: Pending = { ...before, ...patch, at: Date.now() }
  if (patch.pinned) next.archived = false
  if (patch.archived) next.pinned = false
  pending.set(chatId, next)
  changed()
  try {
    await sendState(chatId, patch)
    await refresher?.().catch(() => undefined)
    // A newer change made meanwhile keeps its own entry.
    if (pending.get(chatId) === next) pending.delete(chatId)
  } catch (e) {
    if (pending.get(chatId) === next) {
      if (before) pending.set(chatId, before)
      else pending.delete(chatId)
    }
    throw e
  } finally {
    changed()
  }
}

export function setPinned(chatId: string, pinned: boolean) {
  return updateChatState(chatId, { pinned })
}

export function setArchived(chatId: string, archived: boolean) {
  return updateChatState(chatId, { archived })
}

/** An empty title goes back to the chat's own title. */
export function renameChat(chatId: string, title: string) {
  return updateChatState(chatId, { title: title.trim().slice(0, 120) })
}

/** A deleted chat: drop anything still waiting for it. */
export function forgetChat(chatId: string) {
  if (pending.delete(chatId)) changed()
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

// ── One-time move of the old browser-only state ───────────────────────────

function readLegacy(storage: Pick<Storage, 'getItem'>, userId: string): ChatPrefs | null {
  try {
    const raw = storage.getItem(legacyKey(userId))
    if (!raw) return null
    const v = JSON.parse(raw) as Partial<ChatPrefs>
    return {
      pinned: Array.isArray(v.pinned) ? v.pinned.filter((x): x is string => typeof x === 'string') : [],
      archived: Array.isArray(v.archived) ? v.archived.filter((x): x is string => typeof x === 'string') : [],
      titles:
        v.titles && typeof v.titles === 'object'
          ? Object.fromEntries(
              Object.entries(v.titles).filter((e): e is [string, string] => typeof e[1] === 'string' && !!e[1].trim()),
            )
          : {},
    }
  } catch {
    return null
  }
}

/** The patches the old local state stands for, one per chat. */
export function legacyPatches(prefs: ChatPrefs): Map<string, ChatStatePatch> {
  const out = new Map<string, ChatStatePatch>()
  const at = (id: string) => {
    let p = out.get(id)
    if (!p) out.set(id, (p = {}))
    return p
  }
  for (const id of prefs.archived) at(id).archived = true
  // Pinning wins over archiving, as the old list showed it.
  for (const id of prefs.pinned.slice(0, MAX_PINNED)) {
    const p = at(id)
    p.pinned = true
    delete p.archived
  }
  for (const [id, title] of Object.entries(prefs.titles)) at(id).title = title.trim().slice(0, 120)
  return out
}

const migrating = new Map<string, Promise<number>>()

/**
 * Sends the pins, archives and renames this browser kept before the server
 * had them, then forgets them here. Chats that are gone or no longer readable
 * are dropped; anything that failed for another reason is kept for the next
 * visit. Resolves to how many chats were moved. Runs once per person per visit.
 */
export function migrateLocalChatPrefs(
  userId: string,
  storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> | null = safeLocalStorage(),
  send: (chatId: string, patch: ChatStatePatch) => Promise<unknown> = sendState,
): Promise<number> {
  if (!userId || !storage) return Promise.resolve(0)
  const running = migrating.get(userId)
  if (running) return running
  const job = (async () => {
    const legacy = readLegacy(storage, userId)
    if (!legacy) return 0
    let moved = 0
    const left: ChatPrefs = { pinned: [], archived: [], titles: {} }
    for (const [id, patch] of legacyPatches(legacy)) {
      try {
        await send(id, patch)
        moved++
      } catch (e) {
        if (e instanceof ApiError && (e.status === 400 || e.status === 403 || e.status === 404)) continue
        if (patch.pinned) left.pinned.push(id)
        if (patch.archived) left.archived.push(id)
        if (patch.title) left.titles[id] = patch.title
      }
    }
    try {
      if (left.pinned.length + left.archived.length + Object.keys(left.titles).length === 0) storage.removeItem(legacyKey(userId))
      else storage.setItem(legacyKey(userId), JSON.stringify(left))
    } catch {
      /* blocked storage: nothing more to do */
    }
    return moved
  })()
  migrating.set(userId, job)
  return job
}

function safeLocalStorage(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.localStorage
  } catch {
    return null
  }
}

/** Tests only. */
export function resetChatPrefsForTests() {
  pending.clear()
  migrating.clear()
  refresher = null
  changed()
}

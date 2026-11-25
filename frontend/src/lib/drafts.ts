import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react'

/**
 * Unsent composer text, one per chat, kept in this browser so switching chats
 * or reloading the page never loses what you typed. A successful send clears it.
 */
const PREFIX = 'nexus.draft.'
/** Longer than the composer allows (16k), so a draft is never cut short. */
const MAX_DRAFT = 20000

type DraftStore = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> & { length?: number; key?: (i: number) => string | null }

function defaultStore(): DraftStore | null {
  try {
    return typeof window !== 'undefined' ? window.localStorage : null
  } catch {
    return null
  }
}

export const draftKey = (chatId: string) => PREFIX + chatId

let version = 0
const listeners = new Set<() => void>()
function changed() {
  version++
  for (const l of listeners) l()
}

export function readDraft(chatId: string, store: DraftStore | null = defaultStore()): string {
  if (!chatId || !store) return ''
  try {
    return store.getItem(draftKey(chatId)) ?? ''
  } catch {
    return ''
  }
}

/** Saves the draft; whitespace-only text removes it. */
export function writeDraft(chatId: string, text: string, store: DraftStore | null = defaultStore()) {
  if (!chatId || !store) return
  try {
    if (text.trim()) store.setItem(draftKey(chatId), text.slice(0, MAX_DRAFT))
    else store.removeItem(draftKey(chatId))
  } catch {
    /* storage full or blocked: the draft still lives in the composer */
  }
  changed()
}

export function clearDraft(chatId: string, store: DraftStore | null = defaultStore()) {
  writeDraft(chatId, '', store)
}

export function hasDraft(chatId: string, store: DraftStore | null = defaultStore()) {
  return readDraft(chatId, store).trim().length > 0
}

/** Re-renders when any draft is saved or cleared (for "Draft" markers in the chat list). */
export function useDraftsVersion() {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l)
      const onStorage = (e: StorageEvent) => {
        if (e.key?.startsWith(PREFIX)) changed()
      }
      window.addEventListener('storage', onStorage)
      return () => {
        listeners.delete(l)
        window.removeEventListener('storage', onStorage)
      }
    },
    () => version,
    () => version,
  )
}

/**
 * Composer text for one chat. It starts from the saved draft, saves 300 ms
 * after typing stops, and saves immediately when the chat closes or the page hides.
 */
export function useDraft(chatId: string, delayMs = 300) {
  const [text, setTextState] = useState(() => readDraft(chatId))
  const latest = useRef(text)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const flush = useCallback(() => {
    clearTimeout(timer.current)
    timer.current = undefined
    writeDraft(chatId, latest.current)
  }, [chatId])
  const setText = useCallback(
    (next: string) => {
      latest.current = next
      setTextState(next)
      clearTimeout(timer.current)
      timer.current = setTimeout(flush, delayMs)
    },
    [flush, delayMs],
  )
  /** Empties the composer and forgets the saved draft (after a send). */
  const clear = useCallback(() => {
    clearTimeout(timer.current)
    latest.current = ''
    setTextState('')
    clearDraft(chatId)
  }, [chatId])
  useEffect(() => {
    const onHide = () => {
      if (timer.current !== undefined) flush()
    }
    window.addEventListener('pagehide', onHide)
    return () => {
      window.removeEventListener('pagehide', onHide)
      if (timer.current !== undefined) flush()
    }
  }, [flush])
  return [text, setText, clear] as const
}

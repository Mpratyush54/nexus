import { useSyncExternalStore } from 'react'

/**
 * Desktop relay requests this browser started, by chat id, so the chat list
 * and Phone & devices can show "Running", "Waiting for you" or "Done" without
 * another endpoint. Lives for the tab only.
 */
type Live = { state: string; at: number }

let snapshot = new Map<string, Live>()
const listeners = new Set<() => void>()

export function setLiveChat(chatId: string, state: string) {
  const prev = snapshot.get(chatId)
  if (prev?.state === state) return
  snapshot = new Map(snapshot).set(chatId, { state, at: Date.now() })
  for (const l of listeners) l()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => listeners.delete(l)
}

export function useLiveChats() {
  return useSyncExternalStore(subscribe, () => snapshot, () => snapshot)
}

export type ChatStatus = { label: 'Running' | 'Waiting for you' | 'Done' | 'Stopped'; tone: 'live' | 'wait' | 'done' }

/** An agent counts as running if a request from here is in flight, or the chat moved in the last 3 minutes. */
export function chatStatus(lastActiveAt: string | undefined, live: Live | undefined, now = Date.now()): ChatStatus | null {
  if (live) {
    if (live.state === 'queued' || live.state === 'awaiting_approval') return { label: 'Waiting for you', tone: 'wait' }
    if (live.state === 'running') return { label: 'Running', tone: 'live' }
    if (live.state === 'completed' && now - live.at < 30 * 60_000) return { label: 'Done', tone: 'done' }
    if (live.state === 'cancelled') return now - live.at < 30 * 60_000 ? { label: 'Stopped', tone: 'done' } : null
  }
  if (lastActiveAt && now - Date.parse(lastActiveAt) < 3 * 60_000) return { label: 'Running', tone: 'live' }
  return null
}

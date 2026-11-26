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

/**
 * The leading dot of a chat row, the same as the desktop sidebar's:
 * a spinner while the agent works, a pulsing amber dot when it needs
 * approval, an accent dot for a reply not yet read, red for a problem,
 * a hollow dot while it waits to start or after Nexus closed mid-turn.
 */
export type ChatDot = 'running' | 'approval' | 'queued' | 'unread' | 'error' | 'stopped'

export type ChatStatus = {
  label: 'Running' | 'Waiting for you' | 'Waiting to start' | 'Done' | 'Stopped' | 'New reply' | 'Stopped with a problem' | 'Stopped when Nexus closed'
  tone: 'live' | 'wait' | 'done'
  dot?: ChatDot
}

/** What the person's desktop reports for a chat (GET /v1/chat-status). */
export type ServerChatStatus = { status: string; unread: boolean }

/**
 * A chat's status. In order: a request this browser started; what the
 * person's desktop reports (`server`); and, when the server has no reports
 * (`serverKnown` false: an older server), "Running" for a chat that moved in
 * the last 3 minutes. `active` is the chat open on screen: its new reply is
 * being read.
 */
export function chatStatus(
  lastActiveAt: string | undefined,
  live: Live | undefined,
  now = Date.now(),
  server?: ServerChatStatus,
  serverKnown = false,
  active = false,
): ChatStatus | null {
  if (live) {
    if (live.state === 'queued' || live.state === 'awaiting_approval') return { label: 'Waiting for you', tone: 'wait', dot: 'approval' }
    if (live.state === 'running') return { label: 'Running', tone: 'live', dot: 'running' }
  }
  if (server) {
    switch (server.status) {
      case 'running':
        return { label: 'Running', tone: 'live', dot: 'running' }
      case 'approval':
        return { label: 'Waiting for you', tone: 'wait', dot: 'approval' }
      case 'queued':
        return { label: 'Waiting to start', tone: 'wait', dot: 'queued' }
      case 'interrupted':
        return { label: 'Stopped when Nexus closed', tone: 'done', dot: 'stopped' }
      case 'error':
        if (server.unread && !active) return { label: 'Stopped with a problem', tone: 'done', dot: 'error' }
        break
      case 'done':
      case 'idle':
        if (server.unread && !active) return { label: 'New reply', tone: 'done', dot: 'unread' }
        break
    }
  }
  if (live) {
    if (live.state === 'completed' && now - live.at < 30 * 60_000) return { label: 'Done', tone: 'done' }
    if (live.state === 'cancelled') return now - live.at < 30 * 60_000 ? { label: 'Stopped', tone: 'done' } : null
  }
  if (!serverKnown && lastActiveAt && now - Date.parse(lastActiveAt) < 3 * 60_000) return { label: 'Running', tone: 'live' }
  return null
}

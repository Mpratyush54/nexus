import { useEffect, useMemo } from 'react'
import { useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { chatsApi, type ChatLiveStatus } from '@/api/chats'
import { ApiError } from '@/types/api'
import type { ServerChatStatus } from '@/lib/live-chats'

/**
 * What the person's desktop agents are doing, per chat (Nexus Desktop
 * publishes it to GET /v1/chat-status), for the chat list's status dots.
 * Polled every 10 s while the page is visible; an older server without the
 * endpoint is asked once and then left alone.
 */
const KEY = ['chat-status'] as const

export type ChatLiveStatuses = {
  /** False until the server answered (or when it has no chat status). */
  known: boolean
  byId: ReadonlyMap<string, ServerChatStatus>
}

const EMPTY: ChatLiveStatuses = { known: false, byId: new Map() }

export function useChatLiveStatus(enabled = true): ChatLiveStatuses {
  const q = useQuery({
    queryKey: KEY,
    enabled,
    queryFn: async ({ signal }) => {
      try {
        return (await chatsApi.liveStatus(signal)).items ?? []
      } catch (e) {
        // An older server: no statuses, and no more asking.
        if (e instanceof ApiError && (e.status === 404 || e.status === 405 || e.status === 501)) return null
        throw e
      }
    },
    refetchInterval: (query) => (query.state.data === null ? false : 10_000),
    refetchIntervalInBackground: false,
    retry: 1,
  })
  return useMemo(() => {
    if (!q.data) return EMPTY
    return { known: true, byId: new Map(q.data.map((s) => [s.session_id, { status: s.status, unread: s.unread }])) }
  }, [q.data])
}

/** Clears a chat's unread mark here at once, then on the server (and so the desktop). */
export function markChatRead(cache: QueryClient, chatId: string) {
  cache.setQueryData<ChatLiveStatus[] | null>(KEY, (list) =>
    list ? list.map((s) => (s.session_id === chatId ? { ...s, unread: false } : s)) : list,
  )
  chatsApi.markRead(chatId).catch(() => {
    /* best effort: the next poll shows the server's view */
  })
}

/** Opening a chat whose reply is unread marks it read. */
export function useMarkReadOnOpen(chatId: string, statuses: ChatLiveStatuses) {
  const cache = useQueryClient()
  const unread = !!chatId && !!statuses.byId.get(chatId)?.unread
  useEffect(() => {
    if (unread) markChatRead(cache, chatId)
  }, [cache, chatId, unread])
}

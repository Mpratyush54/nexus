import { apiRequest } from '@/lib/api-client'
import { pageTurnsLocally } from '@/lib/chat-paging'

export type ChatCard = {
  session_id: string; project_id: string; harness: string; native_id: string
  title?: string; summary?: string; last_active_at: string; turn_count: number
  parent_session_id?: string; lineage_kind?: string; visibility?: string
}
export type ChatSession = {
  id: string; project_id: string; harness: string; native_id: string; title?: string
  summary?: string; parent_session_id?: string; lineage_kind?: string; owner_user_id?: string
  workspace_root_hint?: string; last_active_at?: string
}
export type ChatTurn = { idx: number; cursor_idx?: string; role: string; text_preview?: string; tool_calls?: unknown; source_ts?: string; ts?: string }
export type ChatPage<T> = { items: T[]; next_cursor?: string }
export type ShareRecipient = { user_id: string; name: string; project_role?: string; organization_role?: string }

const TURN_PAGE = 100
const turnsPath = (id: string) => `/v1/agent-sessions/${encodeURIComponent(id)}/fetch`
// Per-session transcripts read forward from a server that pages only forward.
// A first-page request (no cursor) always refreshes it.
const fullTranscripts = new Map<string, Promise<ChatTurn[]>>()

function isLegacyTurnPage(page: ChatPage<ChatTurn>) {
  return page.items.length > 0 && page.items.every((turn) => turn.cursor_idx === undefined)
}

async function readForward(id: string, first: ChatPage<ChatTurn>, signal?: AbortSignal) {
  const all = [...first.items]
  let next = first.next_cursor
  for (let guard = 0; next && guard < 1000; guard++) {
    const params = new URLSearchParams({ limit: String(TURN_PAGE), cursor: next })
    const page = await apiRequest<ChatPage<ChatTurn>>(`${turnsPath(id)}?${params}`, { signal })
    all.push(...page.items)
    next = page.next_cursor
  }
  return all
}

export const chatsApi = {
  create(project: string) { return apiRequest<ChatSession>('/v1/agent-sessions', { method: 'POST', body: { project_id: project, harness: 'codex', native_id: `web-${crypto.randomUUID()}`, title: 'New conversation' } }) },
  list(project: string, q: string, cursor: string, signal?: AbortSignal) {
    const params = new URLSearchParams({ project_id: project, limit: '30' })
    if (q.trim()) params.set('q', q.trim())
    if (cursor) params.set('cursor', cursor)
    return apiRequest<ChatPage<ChatCard>>(`/v1/timeline?${params}`, { signal })
  },
  get(id: string, signal?: AbortSignal) { return apiRequest<ChatSession>(`/v1/agent-sessions/${encodeURIComponent(id)}`, { signal }) },
  async turns(id: string, cursor: string, query: string, signal?: AbortSignal) {
    const local = cursor ? fullTranscripts.get(id) : undefined
    if (local) return pageTurnsLocally(await local, cursor, query, TURN_PAGE)
    const params = new URLSearchParams({ limit: String(TURN_PAGE) })
    if (!query.trim()) params.set('direction', 'backward')
    if (cursor) params.set('cursor', cursor)
    if (query.trim()) params.set('q', query.trim())
    const page = await apiRequest<ChatPage<ChatTurn>>(`${turnsPath(id)}?${params}`, { signal })
    if (!isLegacyTurnPage(page)) {
      fullTranscripts.delete(id)
      return page
    }
    // Servers without backward paging or ?q= answer with the oldest page and
    // no cursor_idx. Read the transcript forward once and page it here.
    const all = readForward(id, page, signal)
    fullTranscripts.set(id, all)
    all.catch(() => fullTranscripts.delete(id))
    return pageTurnsLocally(await all, cursor, query, TURN_PAGE)
  },
  summary(id: string, signal?: AbortSignal) { return apiRequest<{ summary?: string }>(`/v1/agent-sessions/${encodeURIComponent(id)}/summary`, { signal }) },
  memory(id: string, signal?: AbortSignal) { return apiRequest<{ items: unknown[] }>(`/v1/agent-sessions/${encodeURIComponent(id)}/memories`, { signal }) },
  fork(id: string) { return apiRequest<ChatSession>(`/v1/agent-sessions/${encodeURIComponent(id)}/fork`, { method: 'POST', body: {} }) },
  recipients(project: string) { return apiRequest<{ people?: ShareRecipient[]; items?: ShareRecipient[] }>(`/v1/sharing/recipients?project_id=${encodeURIComponent(project)}`) },
  /** Owner only: deletes the chat, its turns and shares. Memories made from it stay. */
  remove(id: string) { return apiRequest(`/v1/agent-sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }) },
  share(id: string, userId: string) { return apiRequest(`/v1/agent-sessions/${encodeURIComponent(id)}/grants`, { method: 'POST', body: { user_id: userId } }) },
}

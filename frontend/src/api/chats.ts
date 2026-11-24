import { apiRequest } from '@/lib/api-client'

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

export const chatsApi = {
  create(project: string) { return apiRequest<ChatSession>('/v1/agent-sessions', { method: 'POST', body: { project_id: project, harness: 'codex', native_id: `web-${crypto.randomUUID()}`, title: 'New conversation' } }) },
  list(project: string, q: string, cursor: string, signal?: AbortSignal) {
    const params = new URLSearchParams({ project_id: project, limit: '30' })
    if (q.trim()) params.set('q', q.trim())
    if (cursor) params.set('cursor', cursor)
    return apiRequest<ChatPage<ChatCard>>(`/v1/timeline?${params}`, { signal })
  },
  get(id: string, signal?: AbortSignal) { return apiRequest<ChatSession>(`/v1/agent-sessions/${encodeURIComponent(id)}`, { signal }) },
  turns(id: string, cursor: string, query: string, signal?: AbortSignal) {
    const params = new URLSearchParams({ limit: '100' })
    if (!query.trim()) params.set('direction', 'backward')
    if (cursor) params.set('cursor', cursor)
    if (query.trim()) params.set('q', query.trim())
    return apiRequest<ChatPage<ChatTurn>>(`/v1/agent-sessions/${encodeURIComponent(id)}/fetch?${params}`, { signal })
  },
  summary(id: string, signal?: AbortSignal) { return apiRequest<{ summary?: string }>(`/v1/agent-sessions/${encodeURIComponent(id)}/summary`, { signal }) },
  memory(id: string, signal?: AbortSignal) { return apiRequest<{ items: unknown[] }>(`/v1/agent-sessions/${encodeURIComponent(id)}/memories`, { signal }) },
  fork(id: string) { return apiRequest<ChatSession>(`/v1/agent-sessions/${encodeURIComponent(id)}/fork`, { method: 'POST', body: {} }) },
  recipients(project: string) { return apiRequest<{ items: ShareRecipient[] }>(`/v1/sharing/recipients?project_id=${encodeURIComponent(project)}`) },
  share(id: string, userId: string) { return apiRequest(`/v1/agent-sessions/${encodeURIComponent(id)}/grants`, { method: 'POST', body: { user_id: userId } }) },
}

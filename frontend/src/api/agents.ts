import { apiRequest } from '@/lib/api-client'
import type { ListResponse } from '@/types/api'

export type AgentPermission = {
  project_id: string
  agent_id: string
  mode: 'read_only' | 'propose_only' | 'full' | 'blocked' | string
  rate_limit: number
  tools: Record<string, boolean>
  updated_at?: string
}

export type AgentPutInput = {
  mode: string
  rate_limit?: number
  tools?: Record<string, boolean>
}

export const KNOWN_MCP_TOOLS = [
  'memory_search',
  'memory_write',
  'memory_reflect',
  'episode_search',
  'episode_report',
  'workspace_info',
  'file_read',
  'file_write',
] as const

export type MCPToolCall = {
  id: number | string
  event_type: string
  agent_id?: string
  tool_name?: string
  arguments?: Record<string, unknown>
  result?: Record<string, unknown>
  error?: string
  duration_ms?: number
  session_id?: string
  user_id?: string
  created_at?: string
  payload?: Record<string, unknown>
}

export function normalizeMCPFromWs(msg: {
  event_type?: string
  user_id?: string
  payload?: unknown
}): MCPToolCall | null {
  if (msg.event_type !== 'MCP_TOOL_CALL') return null
  const p = (msg.payload && typeof msg.payload === 'object' ? msg.payload : {}) as Record<string, unknown>
  const inner = (p.payload && typeof p.payload === 'object' ? p.payload : p) as Record<string, unknown>
  const created =
    typeof p.created_at === 'string'
      ? p.created_at
      : typeof inner.created_at === 'string'
        ? inner.created_at
        : new Date().toISOString()
  return {
    id: (p.id as number | string | undefined) ?? `ws-${created}`,
    event_type: 'MCP_TOOL_CALL',
    agent_id: String(p.agent_id ?? inner.agent_id ?? ''),
    tool_name: String(inner.tool_name ?? p.tool_name ?? ''),
    arguments: (inner.arguments ?? p.arguments) as Record<string, unknown> | undefined,
    result: (inner.result ?? p.result) as Record<string, unknown> | undefined,
    error: typeof inner.error === 'string' ? inner.error : typeof p.error === 'string' ? p.error : undefined,
    duration_ms:
      typeof inner.duration_ms === 'number'
        ? inner.duration_ms
        : typeof p.duration_ms === 'number'
          ? p.duration_ms
          : undefined,
    session_id: typeof p.session_id === 'string' ? p.session_id : undefined,
    user_id: typeof p.user_id === 'string' ? p.user_id : msg.user_id,
    created_at: created,
    payload: inner,
  }
}

export const agentsApi = {
  list(projectId: string) {
    return apiRequest<ListResponse<AgentPermission>>(`/projects/${projectId}/agents`)
  },
  get(projectId: string, agentId: string) {
    return apiRequest<AgentPermission>(`/projects/${projectId}/agents/${agentId}`)
  },
  put(projectId: string, agentId: string, input: AgentPutInput) {
    return apiRequest<AgentPermission>(`/projects/${projectId}/agents/${encodeURIComponent(agentId)}`, {
      method: 'PUT',
      body: input,
    })
  },
  remove(projectId: string, agentId: string) {
    return apiRequest(`/projects/${projectId}/agents/${encodeURIComponent(agentId)}`, {
      method: 'DELETE',
    })
  },
  listToolCalls(projectId: string, signal?: AbortSignal) {
    return apiRequest<ListResponse<MCPToolCall>>(`/projects/${projectId}/mcp/tool-calls`, { signal })
  },
}

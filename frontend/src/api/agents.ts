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
}

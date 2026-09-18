import { apiRequest } from '@/lib/api-client'
import type { ListResponse, MemoryItem } from '@/types/api'

export type MemorySearchParams = {
  projectId: string
  q?: string
  tags?: string[]
  level?: string
  limit?: number
}

export const memoryApi = {
  search(params: MemorySearchParams, signal?: AbortSignal) {
    const qs = new URLSearchParams()
    qs.set('project_id', params.projectId)
    if (params.q) qs.set('q', params.q)
    if (params.level) qs.set('level', params.level)
    if (params.limit) qs.set('limit', String(params.limit))
    if (params.tags?.length) qs.set('tags', params.tags.join(','))
    return apiRequest<ListResponse<MemoryItem>>(`/memory/search?${qs}`, { signal })
  },

  confirm(id: string) {
    return apiRequest<MemoryItem>(`/memory/${id}/confirm`, { method: 'POST', body: {} })
  },

  reject(id: string) {
    return apiRequest<MemoryItem>(`/memory/${id}/reject`, { method: 'POST', body: {} })
  },

  promote(id: string) {
    return apiRequest<MemoryItem>(`/memory/${id}/promote`, { method: 'POST', body: {} })
  },
}

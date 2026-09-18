import { apiRequest } from '@/lib/api-client'
import { ApiError, type ListResponse, type MemoryItem, type MemoryUpdatePayload, type MemoryVersion } from '@/types/api'

export type MemorySearchParams = {
  projectId: string
  q?: string
  tags?: string[]
  level?: string
  limit?: number
}

/** Soft-fail helper: 404 means the Phase-2 edit/history API is not wired yet. */
async function gracefulNotFound<T>(fn: () => Promise<T>): Promise<T | null> {
  try {
    return await fn()
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null
    throw err
  }
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

  /** PUT /memory/{id} — returns null when endpoint is missing (404). */
  update(id: string, payload: MemoryUpdatePayload) {
    return gracefulNotFound(() =>
      apiRequest<MemoryItem>(`/memory/${id}`, { method: 'PUT', body: payload }),
    )
  },

  /** GET /memory/{id}/history — returns null when endpoint is missing (404). */
  history(id: string, signal?: AbortSignal) {
    return gracefulNotFound(async () => {
      const data = await apiRequest<ListResponse<MemoryVersion> | MemoryVersion[]>(
        `/memory/${id}/history`,
        { signal },
      )
      if (Array.isArray(data)) return data
      return data.items ?? []
    })
  },

  /** POST /memory/{id}/revert — returns null when endpoint is missing (404). */
  revert(id: string, version: number) {
    return gracefulNotFound(() =>
      apiRequest<MemoryItem>(`/memory/${id}/revert`, {
        method: 'POST',
        body: { version },
      }),
    )
  },
}

import { apiRequest } from '@/lib/api-client'
import { ApiError, type ListResponse, type MemoryItem, type MemoryShareVisibility, type MemoryUpdatePayload, type MemoryVersion } from '@/types/api'

export type MemoryShare = {
  id: string
  memory_id: string
  shared_with_user_id?: string
  shared_with_role?: string
  shared_by?: string
  created_at: string
}

export type MemorySearchParams = {
  projectId: string
  q?: string
  tags?: string[]
  level?: string
  limit?: number
}

export type HarvestJob = {
  id: string
  project_id: string
  status: string
  raw_preview: string
  turn_count: number
  result_count: number
  provider?: string
  error?: string
  source?: string
  created_at: string
  updated_at: string
  turns?: Array<{ speaker?: string; content: string; timestamp?: string }>
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

  /** Raw harvest queue. Pass full=true to include complete turn payloads. */
  harvestQueue(projectId: string, signal?: AbortSignal, full = false) {
    const qs = new URLSearchParams({ project_id: projectId })
    if (full) qs.set('full', '1')
    return apiRequest<{ items: HarvestJob[]; count: number; full?: boolean }>(
      `/memory/harvest?${qs}`,
      { signal },
    )
  },

  harvestJob(id: string, signal?: AbortSignal) {
    return apiRequest<HarvestJob>(`/memory/harvest-jobs/${id}`, { signal })
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
      const data = await apiRequest<
        (ListResponse<MemoryVersion> & { current?: MemoryVersion | null }) | MemoryVersion[]
      >(`/memory/${id}/history`, { signal })
      if (Array.isArray(data)) return { current: null, items: data }
      return { current: data.current ?? null, items: data.items ?? [] }
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

  shares(id: string, signal?: AbortSignal) {
    return apiRequest<ListResponse<MemoryShare>>(`/memory/${id}/shares`, { signal })
  },

  share(
    id: string,
    input: { visibility?: MemoryShareVisibility; user_id?: string; role?: string },
  ) {
    return apiRequest(`/memory/${id}/share`, { method: 'POST', body: input })
  },

  unshare(id: string, userId: string) {
    return apiRequest(`/memory/${id}/share/${userId}`, { method: 'DELETE' })
  },

  copy(id: string, projectId: string) {
    return apiRequest<MemoryItem>(`/memory/${id}/copy`, {
      method: 'POST',
      body: { project_id: projectId },
    })
  },
}

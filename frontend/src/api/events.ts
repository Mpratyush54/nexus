import { apiRequest } from '@/lib/api-client'
import type { ListResponse, ProjectEvent } from '@/types/api'

export type ActivityParams = {
  projectId: string
  type?: string
  userId?: string
  since?: string
  limit?: number
}

export const eventsApi = {
  list(params: ActivityParams, signal?: AbortSignal) {
    const qs = new URLSearchParams()
    if (params.type) qs.set('type', params.type)
    if (params.userId) qs.set('user_id', params.userId)
    if (params.since) qs.set('since', params.since)
    if (params.limit) qs.set('limit', String(params.limit))
    const suffix = qs.toString() ? `?${qs}` : ''
    return apiRequest<ListResponse<ProjectEvent>>(`/projects/${params.projectId}/events${suffix}`, {
      signal,
    })
  },
}

import { apiRequest } from '@/lib/api-client'
import type { ListResponse } from '@/types/api'

export type Session = {
  id: string
  project_id: string
  title?: string
  created_by: string
  is_active: boolean
  created_at: string
  ended_at?: string
}

export type HandoffPackage = {
  id: string
  project_id?: string
  session_id?: string
  from_user?: string
  to_user?: string
  note?: string
  created_at?: string
}

export const sessionsApi = {
  list(projectId: string, activeOnly = false) {
    const q = new URLSearchParams({ project_id: projectId })
    if (activeOnly) q.set('active_only', 'true')
    return apiRequest<ListResponse<Session>>(`/sessions?${q}`)
  },
  create(projectId: string, title: string) {
    return apiRequest<Session>('/sessions', {
      method: 'POST',
      body: { project_id: projectId, title },
    })
  },
  join(sessionId: string) {
    return apiRequest(`/sessions/${sessionId}/join`, { method: 'POST', body: {} })
  },
  leave(sessionId: string) {
    return apiRequest(`/sessions/${sessionId}/leave`, { method: 'POST', body: {} })
  },
  handoff(sessionId: string, input: { to_user: string; note?: string }) {
    return apiRequest<HandoffPackage>(`/sessions/${sessionId}/handoff`, {
      method: 'POST',
      body: {
        to_user: input.to_user,
        note: input.note,
        task: {
          id: 'dashboard',
          title: input.note?.trim() || 'Handoff',
          status: 'in_progress',
        },
      },
    })
  },
  acceptHandoff(sessionId: string, handoffId: string) {
    return apiRequest(`/sessions/${sessionId}/handoff/accept`, {
      method: 'POST',
      body: { handoff_id: handoffId },
    })
  },
  steerInterrupt(sessionId: string, reason?: string) {
    return apiRequest(`/sessions/${sessionId}/steer/interrupt`, {
      method: 'POST',
      body: { reason: reason ?? '' },
    })
  },
  steerPrompt(sessionId: string, prompt: string) {
    return apiRequest(`/sessions/${sessionId}/steer/prompt`, {
      method: 'POST',
      body: { prompt },
    })
  },
  steerResume(sessionId: string) {
    return apiRequest(`/sessions/${sessionId}/steer/resume`, {
      method: 'POST',
      body: {},
    })
  },
}

import { apiRequest } from '@/lib/api-client'
import type { ListResponse } from '@/types/api'

export type AppNotification = {
  id: string
  type: string
  title: string
  body: string
  project_id?: string
  created_at: string
}

export const notificationsApi = {
  list(projectId: string, since?: string) {
    const q = new URLSearchParams({ project_id: projectId })
    if (since) q.set('since', since)
    return apiRequest<ListResponse<AppNotification>>(`/notifications?${q}`)
  },
  vapidPublicKey() {
    return apiRequest<{ public_key: string }>('/notifications/vapid-public-key')
  },
  subscribe(input: { endpoint: string; keys: { p256dh: string; auth: string } }) {
    return apiRequest('/notifications/subscribe', { method: 'POST', body: input })
  },
  unsubscribe(endpoint: string) {
    return apiRequest('/notifications/subscribe', {
      method: 'DELETE',
      body: { endpoint },
    })
  },
}

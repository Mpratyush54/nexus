import { apiRequest } from '@/lib/api-client'
import type { ListResponse } from '@/types/api'

export type AppNotification = {
  id: string
  type: string
  title: string
  body: string
  project_id?: string
  user_id?: string
  href?: string
  created_at: string
}

export type PushDevice = {
  id: string
  host: string
  endpoint_hint?: string
  user_agent?: string
  created_at: string
}

export type NotificationPrefs = {
  types: Record<string, boolean>
}

export const notificationsApi = {
  list(projectId: string, since?: string) {
    const q = new URLSearchParams({ project_id: projectId })
    if (since) q.set('since', since)
    return apiRequest<ListResponse<AppNotification>>(`/notifications?${q}`)
  },
  vapidPublicKey() {
    return apiRequest<{ public_key: string; configured?: boolean }>('/notifications/vapid-public-key')
  },
  subscribe(input: { endpoint: string; keys: { p256dh: string; auth: string } }) {
    return apiRequest<{ subscribed: boolean; id?: string; device?: PushDevice }>(
      '/notifications/subscribe',
      { method: 'POST', body: input },
    )
  },
  unsubscribe(endpoint: string) {
    return apiRequest('/notifications/subscribe', {
      method: 'DELETE',
      body: { endpoint },
    })
  },
  devices() {
    return apiRequest<ListResponse<PushDevice>>('/notifications/devices')
  },
  revokeDevice(id: string) {
    return apiRequest(`/notifications/devices/${id}`, { method: 'DELETE' })
  },
  preferences() {
    return apiRequest<NotificationPrefs>('/notifications/preferences')
  },
  savePreferences(types: Record<string, boolean>) {
    return apiRequest<NotificationPrefs>('/notifications/preferences', {
      method: 'PUT',
      body: { types },
    })
  },
  testPush(input?: { title?: string; body?: string; url?: string; project_id?: string }) {
    return apiRequest<{ sent: number; failed: number; scope: string }>('/notifications/test', {
      method: 'POST',
      body: input ?? {},
    })
  },
}

export function notifSeenKey(projectId: string) {
  return `nexus:notif-seen:${projectId}`
}

export function markNotificationsSeen(projectId: string) {
  if (!projectId) return
  localStorage.setItem(notifSeenKey(projectId), new Date().toISOString())
}

export function unreadNotifications(items: AppNotification[], projectId: string) {
  const seen = localStorage.getItem(notifSeenKey(projectId))
  if (!seen) return items.length
  return items.filter((n) => n.created_at > seen).length
}

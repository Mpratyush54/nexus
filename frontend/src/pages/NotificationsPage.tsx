import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Bell, Smartphone, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import { markNotificationsSeen, notificationsApi, unreadNotifications } from '@/api/notifications'
import { subscribeWebPush } from '@/lib/pwa'
import {
  useNotificationDevices,
  useNotificationPrefs,
  useNotifications,
  useRevokeDevice,
  useSaveNotificationPrefs,
} from '@/hooks/useNotifications'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'

const PREF_LABELS: Record<string, string> = {
  MEMORY_PROPOSED: 'Memory proposed',
  MEMORY_CONFIRMED: 'Memory confirmed',
  MEMORY_UPDATED: 'Memory edited',
  MEMORY_REJECTED: 'Memory rejected',
  MEMORY_SUPERSEDED: 'Memory superseded',
  SESSION_HANDOFF_INITIATED: 'Handoff started',
  SESSION_HANDOFF_ACCEPTED: 'Handoff accepted',
  MCP_TOOL_CALL: 'Agent tool calls',
}

export function NotificationsPage() {
  const { projectId } = useAuth()
  const { push } = useToast()
  const inbox = useNotifications()
  const devices = useNotificationDevices()
  const prefs = useNotificationPrefs()
  const savePrefs = useSaveNotificationPrefs()
  const revoke = useRevokeDevice()
  const [pushBusy, setPushBusy] = useState(false)
  const [thisHint, setThisHint] = useState('')

  useEffect(() => {
    if (projectId && inbox.data) markNotificationsSeen(projectId)
  }, [projectId, inbox.data])

  useEffect(() => {
    void (async () => {
      if (!('serviceWorker' in navigator) || !('PushManager' in window)) return
      const reg = await navigator.serviceWorker.ready
      const sub = await reg.pushManager.getSubscription()
      const endpoint = sub?.endpoint ?? ''
      setThisHint(endpoint.slice(-10))
    })()
  }, [devices.data])

  const items = inbox.data ?? []
  const unread = projectId ? unreadNotifications(items, projectId) : 0
  const types = prefs.data?.types ?? {}

  const onTogglePref = (type: string, enabled: boolean) => {
    savePrefs.mutate(
      { ...types, [type]: enabled },
      {
        onError: (err) =>
          push({
            title: 'Could not save preference',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  const onEnableThisDevice = async () => {
    setPushBusy(true)
    try {
      const vapid = await notificationsApi.vapidPublicKey()
      if (!vapid.configured || !vapid.public_key) {
        push({
          title: 'Web Push not configured',
          detail: 'Set VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY on the server.',
          tone: 'amber',
        })
        return
      }
      const sub = await subscribeWebPush(vapid.public_key)
      if (!sub) {
        push({ title: 'Push not enabled', detail: 'Permission denied or unsupported', tone: 'danger' })
        return
      }
      const json = sub.toJSON()
      await notificationsApi.subscribe({
        endpoint: json.endpoint!,
        keys: {
          p256dh: json.keys?.p256dh ?? '',
          auth: json.keys?.auth ?? '',
        },
      })
      setThisHint((json.endpoint ?? '').slice(-10))
      await devices.refetch()
      push({ title: 'This browser registered', tone: 'teal' })
    } catch (err) {
      push({
        title: 'Device registration failed',
        detail: err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Unknown error',
        tone: 'danger',
      })
    } finally {
      setPushBusy(false)
    }
  }

  const onTestPush = async () => {
    setPushBusy(true)
    try {
      const res = await notificationsApi.testPush({
        title: 'Nexus test',
        body: 'Web Push delivery is working.',
        url: '/app/notifications',
        project_id: projectId ?? undefined,
      })
      push({
        title: 'Test sent',
        detail: `${res.sent} delivered · ${res.failed} failed`,
        tone: res.sent > 0 ? 'teal' : 'amber',
      })
    } catch (err) {
      push({
        title: 'Test push failed',
        detail: err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Unknown error',
        tone: 'danger',
      })
    } finally {
      setPushBusy(false)
    }
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Notifications</h1>
        <p className="mt-1 text-sm text-fg-dim">
          Inbox, per-event preferences, and push devices for this account.
          {unread ? ` ${unread} new.` : ''}
        </p>
      </div>

      <GlassPanel className="space-y-3 p-5">
        <div className="flex items-center justify-between gap-3">
          <h2 className="flex items-center gap-2 text-sm font-medium text-fg">
            <Bell size={14} />
            Inbox
          </h2>
          <StatusPill tone={unread ? 'amber' : 'neutral'}>
            {unread ? `${unread} unread` : 'caught up'}
          </StatusPill>
        </div>
        {inbox.isLoading ? <p className="text-sm text-muted">Loading…</p> : null}
        {!inbox.isLoading && items.length === 0 ? (
          <p className="text-sm text-fg-dim">Nothing yet. Project events will land here.</p>
        ) : null}
        <ul className="divide-y divide-border">
          {items.map((n) => (
            <li key={n.id} className="py-3 first:pt-0 last:pb-0">
              <Link to={n.href || '/app/activity'} className="block hover:text-fg">
                <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                  <p className="text-sm font-medium text-fg">{n.title}</p>
                  <StatusPill>{n.type}</StatusPill>
                  <span className="ml-auto text-[11px] text-muted">{formatRelative(n.created_at)}</span>
                </div>
                <p className="mt-0.5 truncate text-xs text-fg-dim">{n.body}</p>
              </Link>
            </li>
          ))}
        </ul>
      </GlassPanel>

      <GlassPanel className="space-y-3 p-5">
        <h2 className="text-sm font-medium text-fg">Preferences</h2>
        <p className="text-sm text-fg-dim">Choose which event types reach this inbox and Web Push.</p>
        <ul className="space-y-2">
          {Object.keys(PREF_LABELS).map((type) => {
            const on = types[type] !== false
            return (
              <li key={type} className="flex items-center justify-between gap-3">
                <span className="text-sm text-fg">{PREF_LABELS[type]}</span>
                <button
                  type="button"
                  role="switch"
                  aria-checked={on}
                  disabled={savePrefs.isPending}
                  onClick={() => onTogglePref(type, !on)}
                  className={[
                    'relative h-6 w-10 rounded-full border transition',
                    on ? 'border-teal/40 bg-teal-soft' : 'border-border bg-raised',
                  ].join(' ')}
                >
                  <span
                    className={[
                      'absolute top-0.5 h-4 w-4 rounded-full transition',
                      on ? 'left-5 bg-teal' : 'left-0.5 bg-muted',
                    ].join(' ')}
                  />
                </button>
              </li>
            )
          })}
        </ul>
      </GlassPanel>

      <GlassPanel className="space-y-3 p-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="flex items-center gap-2 text-sm font-medium text-fg">
            <Smartphone size={14} />
            Devices
          </h2>
          <div className="flex gap-2">
            <Button size="sm" variant="secondary" disabled={pushBusy} onClick={() => void onTestPush()}>
              Send test
            </Button>
            <Button size="sm" disabled={pushBusy} onClick={() => void onEnableThisDevice()}>
              Enable this browser
            </Button>
          </div>
        </div>
        <p className="text-sm text-fg-dim">
          Registered Web Push endpoints. Keys stay on the server; revoke a row to stop delivery.
        </p>
        {devices.isLoading ? <p className="text-sm text-muted">Loading devices…</p> : null}
        {!devices.isLoading && (devices.data ?? []).length === 0 ? (
          <p className="text-sm text-fg-dim">No devices registered.</p>
        ) : null}
        <ul className="space-y-2">
          {(devices.data ?? []).map((d) => {
            const isThis = thisHint && d.endpoint_hint === thisHint
            return (
              <li
                key={d.id}
                className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-raised px-3 py-2"
              >
                <span className="font-mono text-xs text-fg">{d.host}</span>
                {isThis ? <StatusPill tone="teal">this browser</StatusPill> : null}
                <span className="text-[11px] text-muted">{formatRelative(d.created_at)}</span>
                <Button
                  size="sm"
                  variant="ghost"
                  className="ml-auto"
                  disabled={revoke.isPending}
                  onClick={() =>
                    revoke.mutate(d.id, {
                      onSuccess: () => push({ title: 'Device revoked' }),
                      onError: (err) =>
                        push({
                          title: 'Revoke failed',
                          detail: err instanceof ApiError ? err.message : 'Unknown error',
                          tone: 'danger',
                        }),
                    })
                  }
                >
                  <Trash2 size={12} />
                  Revoke
                </Button>
              </li>
            )
          })}
        </ul>
      </GlassPanel>
    </div>
  )
}

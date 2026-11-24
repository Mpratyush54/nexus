import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronRight, Laptop } from 'lucide-react'
import { useEffect } from 'react'
import { Link, useLocation, useSearchParams } from 'react-router-dom'
import { chatsApi } from '@/api/chats'
import { desktopApi, type DesktopDevice } from '@/api/desktop'
import type { ActiveWorkspace } from '@/api/team'
import { Advanced } from '@/components/Advanced'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { useToast } from '@/components/ui/Toast'
import { usePeople } from '@/hooks/usePeople'
import { useActiveWorkspaces } from '@/hooks/useTeam'
import { apiRequest } from '@/lib/api-client'
import { DESKTOP_RELAY } from '@/lib/env'
import { agentName, chatTitle } from '@/lib/harness-message'
import { chatStatus, useLiveChats } from '@/lib/live-chats'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'
import { SessionsPage } from './SessionsPage'
import { TeleportPage } from './TeleportPage'

type TeleportItem = { id: string; from_user_id: string; preview: string; created_at: string }

const errorText = (err: unknown) =>
  err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Please try again.'

/** A desktop that has checked in recently enough to take a request (the server's own window is 45 s). */
function isOnline(d: DesktopDevice) {
  return Date.now() - new Date(d.last_seen_at).getTime() < 45_000
}

const STATUS_CLASS = {
  live: 'bg-teal/15 text-teal',
  wait: 'bg-amber/15 text-amber',
  done: 'bg-raised text-fg-dim',
}

/** Computers for this project: relay devices when the relay is on, else workspace heartbeats. */
function useComputers() {
  const { projectId } = useAuth()
  const workspaces = useActiveWorkspaces()
  const devices = useQuery({
    queryKey: ['desktop-devices'],
    enabled: DESKTOP_RELAY,
    queryFn: ({ signal }) => desktopApi.devices(signal),
    refetchInterval: 15_000,
    retry: 1,
  })
  const rows: { key: string; name: string; online: boolean; seen?: string }[] = DESKTOP_RELAY
    ? (devices.data?.items ?? [])
        .filter((d) => !projectId || d.projects?.includes(projectId))
        .map((d) => ({ key: d.id, name: d.name, online: isOnline(d), seen: d.last_seen_at }))
    : ((workspaces.data ?? []) as ActiveWorkspace[]).map((w, i) => ({
        key: w.id ?? `${w.machine_id}-${i}`,
        name: w.machine_id || 'Computer',
        online: w.is_online !== false,
        seen: w.last_seen,
      }))
  return { rows, loading: DESKTOP_RELAY ? devices.isLoading : workspaces.isLoading }
}

/**
 * Phone & devices: your computers, the chats on them (tap one to read it and
 * reply in the chat view), and chats a teammate sent you. Session handoffs,
 * session steering, snapshots and sent chats sit under Advanced;
 * /app/sessions and /app/teleport redirect here.
 */
export function DevicesPage() {
  const { projectId } = useAuth()
  const { push } = useToast()
  const qc = useQueryClient()
  const { nameFor } = usePeople()
  const [params] = useSearchParams()
  const { hash } = useLocation()
  const live = useLiveChats()
  const computers = useComputers()
  const online = computers.rows.filter((c) => c.online)
  const chats = useQuery({
    queryKey: ['home-chats', projectId],
    enabled: Boolean(projectId),
    queryFn: ({ signal }) => chatsApi.list(projectId!, '', '', signal),
    select: (page) => page.items.slice(0, 5),
  })
  const inbox = useQuery({
    queryKey: ['teleports', 'inbox'],
    queryFn: ({ signal }) => apiRequest<{ items: TeleportItem[] }>('/v1/teleports/inbox', { signal }),
    retry: 1,
  })
  const accept = useMutation({
    mutationFn: (id: string) => apiRequest(`/v1/teleports/${id}/accept`, { method: 'POST' }),
    onSuccess: async () => {
      push({ title: 'Accepted', detail: 'Open the desktop app to continue this chat.', tone: 'teal' })
      await qc.invalidateQueries({ queryKey: ['teleports'] })
    },
    onError: (err) => push({ title: 'Couldn’t accept', detail: errorText(err), tone: 'danger' }),
  })
  const received = inbox.data?.items ?? []

  useEffect(() => {
    if (!hash) return
    document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' })
  }, [hash])

  if (!projectId) {
    return (
      <div className="space-y-4 py-8">
        <PageHeader title="Phone & devices" description="Pick a project in the sidebar first." />
      </div>
    )
  }

  return (
    <div className="space-y-8">
      <PageHeader
        title="Phone & devices"
        description="Check on the agents working on your computers. Open a chat to read it and reply — from your phone or any browser."
      />

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-fg">Your computers</h2>
        {computers.loading ? <p className="text-sm text-muted">Checking…</p> : null}
        {!computers.loading && computers.rows.length === 0 ? (
          <GlassPanel className="space-y-2 p-4">
            <p className="text-sm text-fg">No computer is connected to this project yet.</p>
            <p className="text-sm text-fg-dim">
              Install the desktop app and open this project’s folder. Your computer shows up here, and you can reply
              to its chats from your phone.
            </p>
            <Link to="/app/connect" className="text-sm text-amber hover:underline">
              Set up the desktop app →
            </Link>
          </GlassPanel>
        ) : null}
        <ul className="grid gap-2 sm:grid-cols-2">
          {computers.rows.map((c) => (
            <li key={c.key}>
              <GlassPanel className="flex items-center gap-3 p-3">
                <Laptop size={18} className="shrink-0 text-fg-dim" aria-hidden />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm text-fg">{c.name}</span>
                  <span className="block text-xs text-muted">
                    {c.online ? 'Online' : `Offline${c.seen ? ` · last seen ${formatRelative(c.seen)}` : ''}`}
                  </span>
                </span>
                <span
                  className={['h-2 w-2 shrink-0 rounded-full', c.online ? 'bg-teal' : 'bg-muted'].join(' ')}
                  aria-hidden
                />
              </GlassPanel>
            </li>
          ))}
        </ul>
      </section>

      <section className="space-y-3">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-sm font-medium text-fg">Chats on your computers</h2>
          <Link to="/app/chats" className="text-xs text-ember hover:underline">
            All chats →
          </Link>
        </div>
        {!online.length && computers.rows.length ? (
          <p className="text-xs text-fg-dim">
            Your computers are offline, so you can read these chats but not reply until Nexus is open again.
          </p>
        ) : null}
        <GlassPanel className="divide-y divide-border">
          {chats.isLoading ? <p className="p-4 text-sm text-muted">Loading…</p> : null}
          {!chats.isLoading && !(chats.data ?? []).length ? (
            <p className="p-4 text-sm text-fg-dim">
              No chats yet. Chat with Claude Code, Codex or Cursor on your computer — they show up here.
            </p>
          ) : null}
          {(chats.data ?? []).map((c) => {
            const st = chatStatus(c.last_active_at, live.get(c.session_id))
            return (
              <Link
                key={c.session_id}
                to={`/app/chats?chat=${encodeURIComponent(c.session_id)}`}
                className="flex items-center gap-3 px-4 py-3 transition hover:bg-raised/50"
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm text-fg">{chatTitle(c.title, c.summary, c.harness)}</span>
                  <span className="block text-xs text-muted">
                    {agentName(c.harness)} · {formatRelative(c.last_active_at)}
                  </span>
                </span>
                {st ? (
                  <span className={`shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium ${STATUS_CLASS[st.tone]}`}>
                    {st.label}
                  </span>
                ) : null}
                <ChevronRight size={16} className="shrink-0 text-muted" aria-hidden />
              </Link>
            )
          })}
        </GlassPanel>
      </section>

      <section id="sent-to-you" className="scroll-mt-6 space-y-3">
        <h2 className="text-sm font-medium text-fg">Chats sent to you</h2>
        {inbox.isLoading ? <p className="text-sm text-muted">Loading…</p> : null}
        {inbox.isError ? <p className="text-sm text-fg-dim">Couldn’t check for chats sent to you. Refresh to try again.</p> : null}
        {!inbox.isLoading && !inbox.isError && received.length === 0 ? (
          <p className="text-sm text-fg-dim">
            Nothing yet. When a teammate sends you a chat from their desktop app, it appears here and you can open it
            on your computer.
          </p>
        ) : null}
        {received.map((item) => (
          <GlassPanel key={item.id} className="flex flex-wrap items-center justify-between gap-3 p-4">
            <span className="min-w-0">
              <span className="block text-sm text-fg">{item.preview || 'A chat'}</span>
              <span className="block text-xs text-muted">
                From {nameFor(item.from_user_id)} · {formatRelative(item.created_at)}
              </span>
            </span>
            <Button size="sm" disabled={accept.isPending} onClick={() => accept.mutate(item.id)}>
              Accept
            </Button>
          </GlassPanel>
        ))}
      </section>

      <Advanced
        title="Advanced"
        summary="Steer a shared live session, hand work to a teammate, restore a saved session, or take back a chat you sent."
        defaultOpen={Boolean(params.get('advanced'))}
      >
        <div className="space-y-10">
          <SessionsPage embedded showSteer />
          <div className="space-y-3">
            <h3 className="text-sm font-medium text-fg">Chats you sent</h3>
            <TeleportPage embedded />
          </div>
        </div>
      </Advanced>
    </div>
  )
}

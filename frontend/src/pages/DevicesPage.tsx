import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Laptop, Pause, Play, Send } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { Link, useLocation, useSearchParams } from 'react-router-dom'
import { Advanced } from '@/components/Advanced'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { useToast } from '@/components/ui/Toast'
import { useSessions, useSteerInterrupt, useSteerPrompt, useSteerResume } from '@/hooks/useSessions'
import { useActiveWorkspaces } from '@/hooks/useTeam'
import { usePeople } from '@/hooks/usePeople'
import { apiRequest } from '@/lib/api-client'
import { useAuth } from '@/providers/AuthProvider'
import type { ActiveWorkspace } from '@/api/team'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'
import { SessionsPage } from './SessionsPage'
import { TeleportPage } from './TeleportPage'

type TeleportItem = { id: string; from_user_id: string; preview: string; created_at: string }

const errorText = (err: unknown) => (err instanceof ApiError ? err.message : 'Please try again.')

/**
 * Phone & devices: one job up top (send a message to an agent that is running
 * on your computer), then the computers that are online, then anything a
 * teammate sent you. Session handoffs, snapshots and sent chats sit under
 * Advanced; /app/sessions and /app/teleport redirect here.
 */
export function DevicesPage() {
  const { projectId } = useAuth()
  const { push } = useToast()
  const qc = useQueryClient()
  const { nameFor } = usePeople()
  const [params] = useSearchParams()
  const { hash } = useLocation()
  const sessions = useSessions()
  const workspaces = useActiveWorkspaces()
  const prompt = useSteerPrompt()
  const pause = useSteerInterrupt()
  const resume = useSteerResume()
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

  const live = (sessions.data ?? []).filter((s) => s.is_active)
  const [sessionId, setSessionId] = useState('')
  const [text, setText] = useState('')
  const chosen = live.find((s) => s.id === sessionId) ?? live[0]
  const computers = ((workspaces.data ?? []) as ActiveWorkspace[]).filter((w) => w.is_online !== false)
  const received = inbox.data?.items ?? []

  useEffect(() => {
    if (!hash) return
    document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' })
  }, [hash])

  const onSend = (e: FormEvent) => {
    e.preventDefault()
    if (!chosen || !text.trim()) return
    prompt.mutate(
      { sessionId: chosen.id, prompt: text.trim() },
      {
        onSuccess: () => {
          push({ title: 'Message sent', detail: `The agent in “${chosen.title || 'this session'}” gets it next.`, tone: 'teal' })
          setText('')
        },
        onError: (err) => push({ title: 'Couldn’t send', detail: errorText(err), tone: 'danger' }),
      },
    )
  }

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
        description="Send a message to an agent that’s working on your computer — from your phone or any browser."
      />

      <GlassPanel className="p-5">
        <form onSubmit={onSend} className="space-y-4">
          <h2 className="text-base font-semibold text-fg">Message a running agent</h2>
          {sessions.isLoading ? (
            <p className="text-sm text-muted" role="status">Looking for running agents…</p>
          ) : live.length === 0 ? (
            <div className="space-y-2 rounded-lg border border-dashed border-border p-4">
              <p className="text-sm text-fg">No agent is running right now.</p>
              <p className="text-sm text-fg-dim">
                Start a chat in the desktop app on your computer. It shows up here, and you can send it messages from
                this page.
              </p>
              <Link to="/app/connect" className="text-sm text-amber hover:underline">
                Set up the desktop app →
              </Link>
            </div>
          ) : (
            <>
              <fieldset className="space-y-2">
                <legend className="mb-1 text-xs font-medium text-fg-dim">1. Pick the agent</legend>
                <div className="grid gap-2 sm:grid-cols-2">
                  {live.map((s) => {
                    const on = chosen?.id === s.id
                    return (
                      <label
                        key={s.id}
                        className={[
                          'flex cursor-pointer items-start gap-3 rounded-lg border px-3 py-2.5 transition',
                          on ? 'border-amber/60 bg-raised' : 'border-border hover:border-border-strong',
                        ].join(' ')}
                      >
                        <input
                          type="radio"
                          name="session"
                          className="mt-1 accent-[var(--color-amber)]"
                          checked={on}
                          onChange={() => setSessionId(s.id)}
                        />
                        <span className="min-w-0">
                          <span className="block truncate text-sm text-fg">{s.title || 'Untitled chat'}</span>
                          <span className="block text-xs text-muted">
                            {nameFor(s.created_by)} · started {formatRelative(s.created_at)}
                          </span>
                        </span>
                      </label>
                    )
                  })}
                </div>
              </fieldset>
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-fg-dim">2. Type your message</span>
                <textarea
                  value={text}
                  onChange={(e) => setText(e.target.value)}
                  rows={3}
                  maxLength={8000}
                  placeholder="e.g. Also add a test for the 402 case"
                  className="w-full rounded-lg border border-border bg-raised px-3 py-2 text-sm text-fg outline-none focus:border-amber"
                />
              </label>
              <div className="flex flex-wrap items-center gap-2">
                <Button type="submit" disabled={!text.trim() || prompt.isPending}>
                  <Send size={15} />
                  {prompt.isPending ? 'Sending…' : 'Send'}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  disabled={!chosen || pause.isPending}
                  onClick={() =>
                    chosen &&
                    pause.mutate(
                      { sessionId: chosen.id, reason: 'paused from the web app' },
                      {
                        onSuccess: () => push({ title: 'Asked the agent to pause', tone: 'amber' }),
                        onError: (err) => push({ title: 'Couldn’t pause', detail: errorText(err), tone: 'danger' }),
                      },
                    )
                  }
                >
                  <Pause size={14} />
                  Pause
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  disabled={!chosen || resume.isPending}
                  onClick={() =>
                    chosen &&
                    resume.mutate(chosen.id, {
                      onSuccess: () => push({ title: 'Agent resumed', tone: 'teal' }),
                      onError: (err) => push({ title: 'Couldn’t resume', detail: errorText(err), tone: 'danger' }),
                    })
                  }
                >
                  <Play size={14} />
                  Resume
                </Button>
              </div>
            </>
          )}
        </form>
      </GlassPanel>

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-fg">Your computers</h2>
        {workspaces.isLoading ? <p className="text-sm text-muted">Checking…</p> : null}
        {!workspaces.isLoading && computers.length === 0 ? (
          <p className="text-sm text-fg-dim">
            No computer is online for this project.{' '}
            <Link to="/app/connect" className="text-amber hover:underline">
              Install the desktop app
            </Link>{' '}
            and open this project’s folder.
          </p>
        ) : null}
        <ul className="grid gap-2 sm:grid-cols-2">
          {computers.map((w, i) => (
            <li key={w.id ?? `${w.machine_id}-${i}`}>
              <GlassPanel className="flex items-center gap-3 p-3">
                <Laptop size={18} className="shrink-0 text-fg-dim" aria-hidden />
                <span className="min-w-0">
                  <span className="block truncate text-sm text-fg">{w.machine_id || 'Computer'}</span>
                  <span className="block text-xs text-muted">
                    Online{w.last_seen ? ` · seen ${formatRelative(w.last_seen)}` : ''}
                  </span>
                </span>
              </GlassPanel>
            </li>
          ))}
        </ul>
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
        summary="Hand work to a teammate, restore a saved session on another computer, or take back a chat you sent."
        defaultOpen={Boolean(params.get('advanced'))}
      >
        <div className="space-y-10">
          <SessionsPage embedded />
          <div className="space-y-3">
            <h3 className="text-sm font-medium text-fg">Chats you sent</h3>
            <TeleportPage embedded />
          </div>
        </div>
      </Advanced>
    </div>
  )
}

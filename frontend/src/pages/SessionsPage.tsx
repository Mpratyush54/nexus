import { motion } from 'framer-motion'
import { Pause, Play, Radio, Send } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import {
  useAcceptHandoff,
  useCreateSession,
  useHandoff,
  useJoinSession,
  useLeaveSession,
  useSessions,
  useSteerInterrupt,
  useSteerPrompt,
  useSteerResume,
} from '@/hooks/useSessions'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'

export function SessionsPage() {
  const { projectId } = useAuth()
  const { push } = useToast()
  const sessions = useSessions()
  const create = useCreateSession()
  const join = useJoinSession()
  const leave = useLeaveSession()
  const handoff = useHandoff()
  const accept = useAcceptHandoff()
  const interrupt = useSteerInterrupt()
  const prompt = useSteerPrompt()
  const resume = useSteerResume()

  const [title, setTitle] = useState('')
  const [selected, setSelected] = useState<string | null>(null)
  const [toUser, setToUser] = useState('')
  const [note, setNote] = useState('')
  const [handoffId, setHandoffId] = useState('')
  const [steerPrompt, setSteerPrompt] = useState('')
  const [steerState, setSteerState] = useState<string>('idle')

  const onCreate = (e: FormEvent) => {
    e.preventDefault()
    create.mutate(title.trim() || 'Untitled session', {
      onSuccess: (s) => {
        push({ title: 'Session opened', detail: s.title || s.id, tone: 'teal' })
        setTitle('')
        setSelected(s.id)
      },
      onError: (err) =>
        push({
          title: 'Create failed',
          detail: err instanceof ApiError ? err.message : 'Unknown error',
          tone: 'danger',
        }),
    })
  }

  if (!projectId) {
    return <div className="py-16 text-sm text-muted">Resolve a project first.</div>
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Sessions</h1>
        <p className="mt-1 text-sm text-fg-dim">
          Handoff context between agents and steer live runs.
        </p>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <GlassPanel className="space-y-4 p-5">
          <h2 className="text-sm font-medium text-fg">Active sessions</h2>
          <form onSubmit={onCreate} className="flex flex-wrap gap-2">
            <input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Session title"
              className="h-10 min-w-[12rem] flex-1 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            />
            <Button type="submit" size="sm" disabled={create.isPending}>
              Open
            </Button>
          </form>

          <ul className="divide-y divide-border">
            {(sessions.data ?? []).map((s, i) => (
              <motion.li
                key={s.id}
                initial={{ opacity: 0, y: 4 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ delay: i * 0.03 }}
                className={[
                  'flex flex-wrap items-center gap-2 py-3',
                  selected === s.id ? 'bg-amber-soft/20 -mx-2 px-2 rounded-lg' : '',
                ].join(' ')}
              >
                <button
                  type="button"
                  className="min-w-0 flex-1 text-left"
                  onClick={() => setSelected(s.id)}
                >
                  <p className="truncate text-sm text-fg">{s.title || 'Untitled'}</p>
                  <p className="font-mono text-[10px] text-muted">
                    {s.id.slice(0, 12)}… · {formatRelative(s.created_at)}
                  </p>
                </button>
                <StatusPill tone={s.is_active ? 'teal' : 'neutral'}>
                  {s.is_active ? 'active' : 'ended'}
                </StatusPill>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={() =>
                    join.mutate(s.id, {
                      onSuccess: () => {
                        setSelected(s.id)
                        push({ title: 'Joined', detail: s.title || s.id })
                      },
                      onError: (err) =>
                        push({
                          title: 'Join failed',
                          detail: err instanceof ApiError ? err.message : 'Unknown error',
                          tone: 'danger',
                        }),
                    })
                  }
                >
                  Join
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={() =>
                    leave.mutate(s.id, {
                      onSuccess: () => push({ title: 'Left session' }),
                    })
                  }
                >
                  Leave
                </Button>
              </motion.li>
            ))}
            {sessions.isLoading ? <li className="py-4 text-sm text-muted">Loading…</li> : null}
            {!sessions.isLoading && !(sessions.data ?? []).length ? (
              <li className="py-4 text-sm text-muted">No sessions yet.</li>
            ) : null}
          </ul>
        </GlassPanel>

        <div className="space-y-6">
          <GlassPanel className="space-y-4 p-5">
            <h2 className="text-sm font-medium text-fg">Handoff</h2>
            {!selected ? (
              <p className="text-sm text-muted">Select a session first.</p>
            ) : (
              <>
                <p className="font-mono text-[10px] text-muted">session {selected}</p>
                <label className="block">
                  <span className="mb-1 block text-xs text-fg-dim">To user id</span>
                  <input
                    value={toUser}
                    onChange={(e) => setToUser(e.target.value)}
                    className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
                  />
                </label>
                <label className="block">
                  <span className="mb-1 block text-xs text-fg-dim">Note / task summary</span>
                  <textarea
                    value={note}
                    onChange={(e) => setNote(e.target.value)}
                    rows={3}
                    className="w-full rounded-lg border border-border bg-raised px-3 py-2 text-sm text-fg outline-none focus:border-amber"
                  />
                </label>
                <Button
                  type="button"
                  size="sm"
                  disabled={!toUser.trim() || handoff.isPending}
                  onClick={() =>
                    handoff.mutate(
                      { sessionId: selected, toUser: toUser.trim(), note: note.trim() },
                      {
                        onSuccess: (pkg) => {
                          setHandoffId(pkg.id)
                          push({
                            title: 'Handoff initiated',
                            detail: pkg.id,
                            tone: 'amber',
                          })
                        },
                        onError: (err) =>
                          push({
                            title: 'Handoff failed',
                            detail: err instanceof ApiError ? err.message : 'Unknown error',
                            tone: 'danger',
                          }),
                      },
                    )
                  }
                >
                  <Send className="h-3.5 w-3.5" />
                  Initiate handoff
                </Button>

                <div className="border-t border-border pt-3">
                  <label className="block">
                    <span className="mb-1 block text-xs text-fg-dim">Accept handoff id</span>
                    <div className="flex gap-2">
                      <input
                        value={handoffId}
                        onChange={(e) => setHandoffId(e.target.value)}
                        className="h-10 min-w-0 flex-1 rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none focus:border-amber"
                      />
                      <Button
                        type="button"
                        size="sm"
                        variant="secondary"
                        disabled={!handoffId.trim() || accept.isPending}
                        onClick={() =>
                          accept.mutate(
                            { sessionId: selected, handoffId: handoffId.trim() },
                            {
                              onSuccess: () =>
                                push({ title: 'Handoff accepted', tone: 'teal' }),
                              onError: (err) =>
                                push({
                                  title: 'Accept failed',
                                  detail:
                                    err instanceof ApiError ? err.message : 'Unknown error',
                                  tone: 'danger',
                                }),
                            },
                          )
                        }
                      >
                        Accept
                      </Button>
                    </div>
                  </label>
                </div>
              </>
            )}
          </GlassPanel>

          <GlassPanel className="space-y-4 p-5">
            <div className="flex items-center gap-2">
              <Radio className="h-4 w-4 text-fg-dim" />
              <h2 className="text-sm font-medium text-fg">Steering HUD</h2>
              <StatusPill tone="amber">{steerState}</StatusPill>
            </div>
            {!selected ? (
              <p className="text-sm text-muted">Select a session to steer.</p>
            ) : (
              <>
                <div className="flex flex-wrap gap-2">
                  <Button
                    type="button"
                    size="sm"
                    variant="secondary"
                    disabled={interrupt.isPending}
                    onClick={() =>
                      interrupt.mutate(
                        { sessionId: selected, reason: 'dashboard interrupt' },
                        {
                          onSuccess: () => {
                            setSteerState('pause_requested')
                            push({ title: 'Interrupt requested', tone: 'amber' })
                          },
                          onError: (err) =>
                            push({
                              title: 'Interrupt failed',
                              detail: err instanceof ApiError ? err.message : 'Unknown error',
                              tone: 'danger',
                            }),
                        },
                      )
                    }
                  >
                    <Pause className="h-3.5 w-3.5" />
                    Interrupt
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    disabled={resume.isPending}
                    onClick={() =>
                      resume.mutate(selected, {
                        onSuccess: () => {
                          setSteerState('running')
                          push({ title: 'Resumed', tone: 'teal' })
                        },
                        onError: (err) =>
                          push({
                            title: 'Resume failed',
                            detail: err instanceof ApiError ? err.message : 'Unknown error',
                            tone: 'danger',
                          }),
                      })
                    }
                  >
                    <Play className="h-3.5 w-3.5" />
                    Resume
                  </Button>
                </div>
                <form
                  className="flex flex-wrap gap-2"
                  onSubmit={(e) => {
                    e.preventDefault()
                    if (!steerPrompt.trim()) return
                    prompt.mutate(
                      { sessionId: selected, prompt: steerPrompt.trim() },
                      {
                        onSuccess: () => {
                          setSteerState('prompted')
                          push({ title: 'Prompt sent' })
                          setSteerPrompt('')
                        },
                        onError: (err) =>
                          push({
                            title: 'Prompt failed',
                            detail: err instanceof ApiError ? err.message : 'Unknown error',
                            tone: 'danger',
                          }),
                      },
                    )
                  }}
                >
                  <input
                    value={steerPrompt}
                    onChange={(e) => setSteerPrompt(e.target.value)}
                    placeholder="Inject steering prompt…"
                    className="h-10 min-w-[12rem] flex-1 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
                  />
                  <Button type="submit" size="sm" disabled={prompt.isPending}>
                    Prompt
                  </Button>
                </form>
              </>
            )}
          </GlassPanel>
        </div>
      </div>
    </div>
  )
}

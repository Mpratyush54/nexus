import { Bot, Layers, Users } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Advanced } from '@/components/Advanced'
import { PageHeader } from '@/components/PageHeader'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { useActivity } from '@/hooks/useActivity'
import { usePeople } from '@/hooks/usePeople'
import { dayLabel, describeEvent, type PlainEvent } from '@/lib/plain-language'
import { useAuth } from '@/providers/AuthProvider'
import { formatRelative } from '@/utils/format'
import { AgentsPage } from './AgentsPage'

type Show = 'all' | 'agent' | 'memory'

const SHOW: [Show, string][] = [
  ['all', 'Everything'],
  ['agent', 'Agents'],
  ['memory', 'Memory changes'],
]

const KIND_ICON = { agent: Bot, memory: Layers, people: Users }

/**
 * One timeline of what happened in this project, written as sentences:
 * "Claude Code searched project memory · 26m ago". The old Agents page (MCP
 * tokens, modes, rate limits) lives under Advanced; /app/agents redirects here.
 */
export function ActivityPage() {
  const { projectId, user } = useAuth()
  const { nameFor, people } = usePeople()
  const [params] = useSearchParams()
  const [show, setShow] = useState<Show>('all')
  const [userId, setUserId] = useState('')
  const [since, setSince] = useState('')
  const feed = useActivity('', userId.trim(), since)

  const rows = useMemo(() => {
    const out: { day: string; items: { id: number; at: string; ev: PlainEvent }[] }[] = []
    const sorted = [...(feed.data ?? [])].sort((a, b) => b.created_at.localeCompare(a.created_at))
    for (const raw of sorted) {
      const ev = describeEvent(raw)
      if (show === 'agent' && ev.kind !== 'agent') continue
      if (show === 'memory' && ev.kind !== 'memory') continue
      const day = dayLabel(raw.created_at)
      let group = out[out.length - 1]
      if (!group || group.day !== day) {
        group = { day, items: [] }
        out.push(group)
      }
      group.items.push({ id: raw.id, at: raw.created_at, ev })
    }
    return out
  }, [feed.data, show])

  const filtersOn = show !== 'all' || Boolean(userId) || Boolean(since)

  return (
    <div className="space-y-8">
      <PageHeader
        title="Activity"
        description={
          projectId
            ? 'What your agents and your team did in this project, newest first.'
            : 'Pick a project in the sidebar to see its activity.'
        }
      />

      <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Show">
        {SHOW.map(([value, label]) => (
          <button
            key={value}
            type="button"
            aria-pressed={show === value}
            onClick={() => setShow(value)}
            className={[
              'rounded-full border px-3 py-1.5 text-xs font-medium transition',
              show === value ? 'border-amber/60 bg-raised text-fg' : 'border-border text-fg-dim hover:text-fg',
            ].join(' ')}
          >
            {label}
          </button>
        ))}
      </div>

      {feed.isLoading ? <p className="text-sm text-muted" role="status">Loading activity…</p> : null}
      {feed.isError ? (
        <p className="text-sm text-danger" role="alert">
          Couldn’t load activity.{' '}
          <button type="button" className="underline" onClick={() => void feed.refetch()}>
            Try again
          </button>
        </p>
      ) : null}
      {projectId && !feed.isLoading && !feed.isError && rows.length === 0 ? (
        <GlassPanel className="space-y-2 p-6">
          {filtersOn ? (
            <p className="text-sm text-fg-dim">Nothing matches. Try “Everything”.</p>
          ) : (
            <>
              <p className="text-sm text-fg">Nothing has happened here yet.</p>
              <p className="text-sm text-fg-dim">
                When your agents look things up or save what they learned, each step shows up here.
              </p>
              <Link to="/app/connect" className="text-sm text-amber hover:underline">
                Set up the desktop app →
              </Link>
            </>
          )}
        </GlassPanel>
      ) : null}

      <div className="space-y-6">
        {rows.map((group) => (
          <section key={group.day} className="space-y-2">
            <h2 className="text-xs font-medium uppercase tracking-wide text-muted">{group.day}</h2>
            <GlassPanel className="divide-y divide-border">
              {group.items.map(({ id, at, ev }) => {
                const Icon = KIND_ICON[ev.kind]
                const who = ev.agent ?? (ev.userId ? nameFor(ev.userId) : 'Someone')
                return (
                  <div key={id} className="flex items-start gap-3 px-4 py-3">
                    <Icon size={16} className="mt-0.5 shrink-0 text-fg-dim" aria-hidden />
                    <p className="min-w-0 flex-1 text-sm text-fg-dim">
                      <span className="font-medium text-fg">{who}</span> {ev.action}
                      {ev.object ? (
                        <>
                          : <span className="text-fg">{ev.object}</span>
                        </>
                      ) : null}
                    </p>
                    <time className="shrink-0 text-xs text-muted" dateTime={at}>
                      {formatRelative(at)}
                    </time>
                  </div>
                )
              })}
            </GlassPanel>
          </section>
        ))}
      </div>

      {projectId ? (
        <Advanced
          title="Advanced"
          summary="Filter by person or date, and manage how agents connect to Nexus (access keys, permissions, limits)."
          defaultOpen={params.get('advanced') === 'agents'}
        >
          <div className="space-y-10">
            <div className="flex flex-wrap items-end gap-2">
              <label className="block min-w-[12rem]">
                <span className="mb-1 block text-xs text-fg-dim">Person</span>
                <select
                  value={userId}
                  onChange={(e) => setUserId(e.target.value)}
                  className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
                >
                  <option value="">Everyone</option>
                  {user?.userId ? <option value={user.userId}>You</option> : null}
                  {people.map((p) => (
                    <option key={p.user_id} value={p.user_id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </label>
              <label className="block">
                <span className="mb-1 block text-xs text-fg-dim">Since</span>
                <input
                  type="date"
                  value={since}
                  onChange={(e) => setSince(e.target.value)}
                  className="h-10 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
                />
              </label>
            </div>
            <div className="space-y-3">
              <h3 className="text-sm font-medium text-fg">Agent connections</h3>
              <p className="text-xs text-fg-dim">
                Only needed if you connect an agent to Nexus yourself. The desktop app saves chats without this.
              </p>
              <AgentsPage embedded />
            </div>
          </div>
        </Advanced>
      ) : null}
    </div>
  )
}

import { motion } from 'framer-motion'
import { Activity, Bot, FolderGit2, Layers, Users } from 'lucide-react'
import { Link } from 'react-router-dom'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useDashboard } from '@/hooks/useDashboard'
import { useAuth } from '@/providers/AuthProvider'
import { formatRelative } from '@/utils/format'
import type { HeatDay } from '@/api/dashboard'

function heatLevel(count: number, max: number) {
  if (count <= 0) return 0
  if (max <= 1) return 3
  const r = count / max
  if (r < 0.25) return 1
  if (r < 0.5) return 2
  if (r < 0.75) return 3
  return 4
}

function Heatmap({ days }: { days: HeatDay[] }) {
  const max = days.reduce((m, d) => Math.max(m, d.count), 0)
  // pad to weeks starting Sunday
  const first = days[0] ? new Date(days[0].date + 'T12:00:00Z') : new Date()
  const pad = first.getUTCDay()
  const cells: (HeatDay | null)[] = [...Array(pad).fill(null), ...days]

  return (
    <div className="overflow-x-auto">
      <div
        className="inline-grid gap-[3px]"
        style={{ gridTemplateRows: 'repeat(7, 11px)', gridAutoFlow: 'column', gridAutoColumns: '11px' }}
      >
        {cells.map((d, i) => {
          if (!d) {
            return <span key={`pad-${i}`} className="h-[11px] w-[11px] rounded-[2px] bg-transparent" />
          }
          const lvl = heatLevel(d.count, max)
          return (
            <span
              key={d.date}
              title={`${d.date}: ${d.count} events`}
              className={[
                'h-[11px] w-[11px] rounded-[2px]',
                lvl === 0 && 'bg-raised',
                lvl === 1 && 'bg-amber/25',
                lvl === 2 && 'bg-amber/45',
                lvl === 3 && 'bg-amber/70',
                lvl === 4 && 'bg-amber',
              ]
                .filter(Boolean)
                .join(' ')}
            />
          )
        })}
      </div>
      <p className="mt-2 text-[11px] text-muted">
        Last year · darker amber = more project activity
      </p>
    </div>
  )
}

function Metric({
  label,
  value,
  hint,
  icon: Icon,
}: {
  label: string
  value: string | number
  hint?: string
  icon: typeof Layers
}) {
  return (
    <GlassPanel className="p-4">
      <div className="flex items-start justify-between gap-2">
        <p className="text-[11px] uppercase tracking-wide text-muted">{label}</p>
        <Icon size={14} className="text-fg-dim" />
      </div>
      <p className="mt-2 text-2xl font-semibold tracking-tight text-fg">{value}</p>
      {hint ? <p className="mt-1 text-xs text-fg-dim">{hint}</p> : null}
    </GlassPanel>
  )
}

export function DashboardPage() {
  const { projectId } = useAuth()
  const dash = useDashboard()
  const m = dash.data?.metrics

  if (!projectId) {
    return (
      <div className="py-16 text-sm text-muted">Resolve a project first (sign in again if needed).</div>
    )
  }

  return (
    <div className="space-y-8">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-fg">Dashboard</h1>
          <p className="mt-1 text-sm text-fg-dim">
            Project pulse — memories, people, agents, and a year of activity.
          </p>
        </div>
        {m?.github_connected && m.github_repo ? (
          <a
            href={`https://github.com/${m.github_repo}`}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 text-xs text-fg-dim underline decoration-border underline-offset-2 hover:text-fg"
          >
            <FolderGit2 size={13} />
            {m.github_repo}
          </a>
        ) : (
          <Link to="/app/team" className="text-xs text-amber hover:underline">
            Connect GitHub →
          </Link>
        )}
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Metric
          label="Memories"
          value={m?.memories ?? '—'}
          hint={m ? `${m.confirmed} confirmed · ${m.proposed} proposed` : undefined}
          icon={Layers}
        />
        <Metric label="Team" value={m?.members ?? '—'} hint={m ? `${m.online} online now` : undefined} icon={Users} />
        <Metric label="Events (7d)" value={m?.events_7d ?? '—'} hint="Memory, sessions, agents" icon={Activity} />
        <Metric label="Agent calls" value={m?.agent_calls ?? '—'} hint="MCP tool activity" icon={Bot} />
      </div>

      <GlassPanel className="space-y-4 p-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-medium text-fg">Activity</h2>
          <StatusPill tone="accent">contribution graph</StatusPill>
        </div>
        {dash.isLoading ? (
          <p className="text-sm text-muted">Loading heatmap…</p>
        ) : (
          <Heatmap days={dash.data?.heatmap ?? []} />
        )}
      </GlassPanel>

      <div className="grid gap-4 lg:grid-cols-2">
        <GlassPanel className="space-y-3 p-5">
          <h2 className="text-sm font-medium text-fg">By type</h2>
          <ul className="space-y-2">
            {(dash.data?.by_type ?? []).map((row, i) => (
              <motion.li
                key={row.type}
                initial={{ opacity: 0, x: -6 }}
                animate={{ opacity: 1, x: 0 }}
                transition={{ delay: i * 0.03 }}
                className="flex items-center justify-between gap-2 text-sm"
              >
                <span className="truncate font-mono text-xs text-fg-dim">{row.type}</span>
                <span className="text-fg">{row.count}</span>
              </motion.li>
            ))}
            {!dash.data?.by_type?.length ? (
              <li className="text-sm text-muted">No events yet — propose a memory to start the graph.</li>
            ) : null}
          </ul>
        </GlassPanel>

        <GlassPanel className="space-y-3 p-5">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-medium text-fg">Recent</h2>
            <Link to="/app/activity" className="text-xs text-fg-dim hover:text-fg">
              Full feed →
            </Link>
          </div>
          <ul className="divide-y divide-border">
            {(dash.data?.recent ?? []).map((ev) => (
              <li key={ev.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5 text-sm">
                <span className="font-mono text-[11px] text-fg">{ev.event_type}</span>
                <span className="text-xs text-muted">{formatRelative(ev.created_at)}</span>
              </li>
            ))}
            {!dash.data?.recent?.length ? (
              <li className="py-4 text-sm text-muted">Nothing yet.</li>
            ) : null}
          </ul>
        </GlassPanel>
      </div>
    </div>
  )
}

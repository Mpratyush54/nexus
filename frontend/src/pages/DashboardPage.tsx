import { motion } from 'framer-motion'
import {
  Activity,
  Bot,
  Cable,
  Layers,
  RefreshCw,
  ScanSearch,
  Users,
} from 'lucide-react'
import { Link } from 'react-router-dom'
import { ProjectSwitcher } from '@/components/ProjectSwitcher'
import { ActivityHeatmap } from '@/components/ActivityHeatmap'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { Button } from '@/components/ui/Button'
import { useToast } from '@/components/ui/Toast'
import { useDashboard } from '@/hooks/useDashboard'
import { useMemorySearch } from '@/hooks/useMemory'
import {
  useLocalHarvest,
  useTriggerHarvestScan,
} from '@/hooks/useDaemon'
import { useCurrentProject } from '@/hooks/useProjects'
import { useProjectSummaries } from '@/hooks/useProjectSummaries'
import { useFollowHarvestProject } from '@/hooks/useFollowHarvestProject'
import { useTrustedLocalBridge } from '@/hooks/useTrustedLocalBridge'
import { useAuth } from '@/providers/AuthProvider'
import { formatRelative } from '@/utils/format'

function shortTime(iso?: string) {
  if (!iso) return '—'
  try {
    return formatRelative(iso)
  } catch {
    return iso
  }
}

function projectLabelOf(p: { display_name?: string; folder_name?: string; id: string }) {
  return p.display_name || p.folder_name || p.id.slice(0, 8)
}

export function DashboardPage() {
  const { projectId, setProjectId } = useAuth()
  const { current } = useCurrentProject()
  const summaries = useProjectSummaries()
  const dash = useDashboard()
  const memories = useMemorySearch('')
  const bridge = useTrustedLocalBridge()
  const bridgeUrl = bridge.bridgeUrl
  const harvest = useLocalHarvest(bridgeUrl)
  const scanNow = useTriggerHarvestScan(bridgeUrl)
  const { push } = useToast()
  useFollowHarvestProject(harvest.data, bridge.trusted)

  const m = dash.data?.metrics
  const hs = harvest.data
  const projectLabel =
    current?.display_name || current?.folder_name || (projectId ? `${projectId.slice(0, 8)}…` : 'No project')
  const harvestTarget =
    hs?.project_id && projectId && hs.project_id !== projectId
      ? 'mismatch'
      : hs?.project_id
        ? 'matched'
        : 'unknown'
  const harvestProjectRow = (summaries.data ?? []).find((s) => s.project.id === hs?.project_id)

  const memoryItems = memories.data ?? []
  const proposed = memoryItems.filter((x) => x.status === 'PROPOSED').slice(0, 8)
  const confirmed = memoryItems.filter((x) => x.status === 'CONFIRMED').slice(0, 8)
  const shown = proposed.length ? proposed : confirmed

  const richer = (summaries.data ?? []).find(
    (s) =>
      s.project.id !== projectId &&
      s.metrics.proposed + s.metrics.memories >
        (m?.proposed ?? 0) + (m?.memories ?? 0) + 5,
  )

  if (!projectId) {
    return (
      <div className="space-y-6 py-8">
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Home</h1>
        <p className="text-sm text-fg-dim">Pick or create a project to see memories and harvest.</p>
        <ProjectSwitcher />
        <Link to="/app/org" className="text-sm text-amber hover:underline">
          Open Org →
        </Link>
      </div>
    )
  }

  return (
    <div className="space-y-8">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0 flex-1">
          <p className="text-[11px] uppercase tracking-wide text-muted">Working on</p>
          <h1 className="mt-1 truncate text-2xl font-semibold tracking-tight text-fg">{projectLabel}</h1>
          <p className="mt-1 text-sm text-fg-dim">
            Memories are per project — switch below if this folder looks empty
          </p>
        </div>
        <div className="w-full max-w-xs sm:w-56">
          <ProjectSwitcher compact />
        </div>
      </div>

      {harvestTarget === 'mismatch' && hs?.project_id ? (
        <GlassPanel className="flex flex-wrap items-center justify-between gap-3 border border-amber/40 p-4">
          <div>
            <p className="text-sm text-fg">
              Desktop is uploading memories to{' '}
              <span className="font-medium">
                {harvestProjectRow ? projectLabelOf(harvestProjectRow.project) : 'another project'}
              </span>
              , not the one selected here ({projectLabel}).
            </p>
            <p className="mt-1 text-xs text-fg-dim">
              Folder <span className="font-mono">central-memory</span> often maps to git remote{' '}
              <span className="font-mono">nexus</span> — switch to see live harvest.
            </p>
          </div>
          <Button type="button" size="sm" onClick={() => setProjectId(hs.project_id!)}>
            Show harvest project
          </Button>
        </GlassPanel>
      ) : null}

      {richer ? (
        <GlassPanel className="flex flex-wrap items-center justify-between gap-3 border border-amber/30 p-4">
          <div>
            <p className="text-sm text-fg">
              <span className="font-medium">{projectLabelOf(richer.project)}</span> has{' '}
              {richer.metrics.proposed > 0
                ? `${richer.metrics.proposed} proposed`
                : `${richer.metrics.memories} memories`}{' '}
              — more than this project.
            </p>
            <p className="mt-1 text-xs text-fg-dim">Harvest and Memory pages only show the active project.</p>
          </div>
          <Button
            type="button"
            size="sm"
            onClick={() => setProjectId(richer.project.id)}
          >
            Switch to {projectLabelOf(richer.project)}
          </Button>
        </GlassPanel>
      ) : null}

      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-medium text-fg">Your projects</h2>
          <p className="text-[11px] text-muted">
            {(summaries.data ?? []).length || '…'} registered · folders on disk need Desktop bind / resolve to appear
          </p>
        </div>
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {(summaries.data ?? []).map((row) => {
            const active = row.project.id === projectId
            return (
              <button
                key={row.project.id}
                type="button"
                onClick={() => setProjectId(row.project.id)}
                className={[
                  'rounded-lg border px-3 py-3 text-left transition',
                  active
                    ? 'border-amber/50 bg-raised'
                    : 'border-border bg-raised/40 hover:border-border-strong',
                ].join(' ')}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate text-sm font-medium text-fg">
                    {projectLabelOf(row.project)}
                  </span>
                  {active ? <StatusPill tone="amber">active</StatusPill> : null}
                </div>
                <p className="mt-1 truncate font-mono text-[10px] text-muted">
                  {row.project.folder_name || row.project.id.slice(0, 12)}
                </p>
                <p className="mt-2 text-xs text-fg-dim">
                  {row.metrics.memories} memories
                  {row.metrics.proposed ? ` · ${row.metrics.proposed} proposed` : ''}
                </p>
              </button>
            )
          })}
          {summaries.isLoading ? (
            <p className="text-xs text-muted sm:col-span-2 lg:col-span-3">Loading project counts…</p>
          ) : null}
        </div>
      </section>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <GlassPanel className="p-4">
          <p className="text-[11px] uppercase tracking-wide text-muted">Memories</p>
          <p className="mt-2 text-2xl font-semibold text-fg">{m?.memories ?? memoryItems.length}</p>
          <p className="mt-1 text-xs text-fg-dim">
            {m ? `${m.confirmed} confirmed · ${m.proposed} proposed` : 'Loading…'}
          </p>
        </GlassPanel>
        <GlassPanel className="p-4">
          <p className="text-[11px] uppercase tracking-wide text-muted">Harvest</p>
          <p className="mt-2 text-2xl font-semibold text-fg">{hs?.proposals_saved ?? 0}</p>
          <p className="mt-1 text-xs text-fg-dim">
            {hs?.running ? `Last scan ${shortTime(hs.last_scan_at)}` : 'Desktop offline'}
          </p>
        </GlassPanel>
        <GlassPanel className="p-4">
          <p className="text-[11px] uppercase tracking-wide text-muted">Team</p>
          <p className="mt-2 text-2xl font-semibold text-fg">{m?.members ?? '—'}</p>
          <p className="mt-1 text-xs text-fg-dim">{m ? `${m.online} online` : ''}</p>
        </GlassPanel>
        <GlassPanel className="p-4">
          <p className="text-[11px] uppercase tracking-wide text-muted">Agents</p>
          <p className="mt-2 text-2xl font-semibold text-fg">{m?.agent_calls ?? '—'}</p>
          <p className="mt-1 text-xs text-fg-dim">MCP tool calls</p>
        </GlassPanel>
      </div>

      <GlassPanel className="space-y-3 p-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <Activity size={16} className="text-amber" />
            <h2 className="text-sm font-medium text-fg">Activity</h2>
          </div>
          <Link to="/app/activity" className="text-xs text-ember hover:underline">
            Full log →
          </Link>
        </div>
        <ActivityHeatmap days={dash.data?.heatmap} loading={dash.isLoading} />
      </GlassPanel>

      <div className="grid gap-6 lg:grid-cols-5">
        <GlassPanel className="space-y-4 p-5 lg:col-span-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2">
              <Layers size={16} className="text-amber" />
              <h2 className="text-sm font-medium text-fg">
                {proposed.length ? 'Needs review' : 'Project memories'}
              </h2>
              {proposed.length ? (
                <StatusPill tone="amber">{`${proposed.length} proposed`}</StatusPill>
              ) : null}
            </div>
            <Link to="/app/memory" className="text-xs text-ember hover:underline">
              Open Memory →
            </Link>
          </div>

          <ul className="divide-y divide-border">
            {shown.map((item, i) => (
              <motion.li
                key={item.id}
                initial={{ opacity: 0, y: 4 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ delay: i * 0.02 }}
                className="py-3"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="font-mono text-xs text-fg">{item.key}</span>
                  <StatusPill tone={item.status === 'PROPOSED' ? 'amber' : 'teal'}>
                    {item.status || '—'}
                  </StatusPill>
                </div>
                <p className="mt-1 line-clamp-2 text-sm text-fg-dim">{item.content}</p>
                <p className="mt-1 text-[11px] text-muted">
                  {item.source || 'unknown'} · {item.created_at ? formatRelative(item.created_at) : ''}
                </p>
              </motion.li>
            ))}
            {!shown.length ? (
              <li className="py-8 text-center text-sm text-muted">
                No memories in this project yet. Chat in Cursor/Claude with the desktop daemon online,
                or write via MCP — then confirm them in Memory.
              </li>
            ) : null}
          </ul>
        </GlassPanel>

        <GlassPanel className="space-y-4 p-5 lg:col-span-2">
          <div className="flex items-center justify-between gap-2">
            <div className="flex items-center gap-2">
              <ScanSearch size={16} className="text-amber" />
              <h2 className="text-sm font-medium text-fg">Scanner</h2>
            </div>
            <StatusPill tone={hs?.running ? 'teal' : 'danger'}>
              {hs?.running ? 'live' : 'offline'}
            </StatusPill>
          </div>

          <p className="text-xs text-fg-dim">
            {hs?.message ||
              'Start Nexus Desktop so this page can show scans for agent chats on your machine.'}
          </p>

          <div className="space-y-2 rounded-lg border border-border bg-raised/40 px-3 py-2.5 text-xs text-fg-dim">
            <p>
              <span className="text-muted">Portal project</span>{' '}
              <span className="font-mono text-fg">{projectId.slice(0, 8)}…</span>
            </p>
            <p>
              <span className="text-muted">Daemon target</span>{' '}
              <span className="font-mono text-fg">
                {hs?.project_id ? `${hs.project_id.slice(0, 8)}…` : '—'}
              </span>
              {harvestTarget === 'mismatch' ? (
                <StatusPill tone="amber">different project</StatusPill>
              ) : harvestTarget === 'matched' ? (
                <StatusPill tone="teal">same project</StatusPill>
              ) : null}
            </p>
            <p>
              <span className="text-muted">Folder</span>{' '}
              <span className="font-mono text-fg">{hs?.root || bridge.local.data?.status.root || '—'}</span>
            </p>
            <p>
              <span className="text-muted">Last scan</span> {shortTime(hs?.last_scan_at)} ·{' '}
              {hs?.last_scan_files ?? 0} files · {hs?.last_scan_turns ?? 0} turns
            </p>
          </div>

          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              size="sm"
              disabled={!bridgeUrl || scanNow.isPending}
              onClick={() =>
                scanNow.mutate(undefined, {
                  onSuccess: (st) =>
                    push({
                      title: 'Scan complete',
                      detail: st.message || `${st.last_scan_files ?? 0} files`,
                      tone: 'teal',
                    }),
                  onError: (err) =>
                    push({
                      title: 'Scan failed',
                      detail: err instanceof Error ? err.message : 'Daemon offline',
                      tone: 'danger',
                    }),
                })
              }
            >
              <ScanSearch size={14} />
              {scanNow.isPending ? 'Scanning…' : 'Scan now'}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="secondary"
              onClick={() => {
                void harvest.refetch()
                void bridge.local.refetch()
                void memories.refetch()
              }}
            >
              <RefreshCw size={14} />
              Refresh
            </Button>
            <Link to="/app/connect">
              <Button type="button" size="sm" variant="ghost">
                <Cable size={14} />
                Desktop setup
              </Button>
            </Link>
          </div>

          <div>
            <p className="mb-1.5 text-[11px] font-medium text-fg">Recent daemon events</p>
            <ul className="max-h-40 space-y-1 overflow-auto text-[11px] text-fg-dim">
              {[...(hs?.recent ?? [])].reverse().slice(0, 8).map((line, i) => (
                <li key={`${line.at}-${i}`} className="truncate">
                  <span className="text-muted">{shortTime(line.at)}</span> {line.type}{' '}
                  {line.detail}
                </li>
              ))}
              {!hs?.recent?.length ? (
                <li className="text-muted">No scan events yet.</li>
              ) : null}
            </ul>
          </div>
        </GlassPanel>
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <GlassPanel className="space-y-3 p-5">
          <div className="flex items-center gap-2">
            <Activity size={14} className="text-amber" />
            <h2 className="text-sm font-medium text-fg">Recent activity</h2>
          </div>
          <ul className="divide-y divide-border text-sm">
            {(dash.data?.recent ?? []).slice(0, 6).map((ev) => (
              <li key={ev.id} className="flex justify-between gap-2 py-2">
                <span className="truncate font-mono text-[11px] text-fg">{ev.event_type}</span>
                <span className="shrink-0 text-xs text-muted">{formatRelative(ev.created_at)}</span>
              </li>
            ))}
            {!dash.data?.recent?.length ? (
              <li className="py-3 text-sm text-muted">Nothing yet for this project.</li>
            ) : null}
          </ul>
          <Link to="/app/activity" className="text-xs text-ember hover:underline">
            Full activity →
          </Link>
        </GlassPanel>

        <GlassPanel className="space-y-3 p-5">
          <div className="flex items-center gap-2">
            <Users size={14} className="text-amber" />
            <h2 className="text-sm font-medium text-fg">People</h2>
          </div>
          <p className="text-sm text-fg-dim">
            {m?.members ?? 0} members · {m?.online ?? 0} online on this project
          </p>
          <Link to="/app/team" className="text-xs text-ember hover:underline">
            Team →
          </Link>
        </GlassPanel>

        <GlassPanel className="space-y-3 p-5">
          <div className="flex items-center gap-2">
            <Bot size={14} className="text-amber" />
            <h2 className="text-sm font-medium text-fg">Agents</h2>
          </div>
          <p className="text-sm text-fg-dim">
            {m?.agent_calls ?? 0} MCP calls. Auto-harvest does not need MCP.
          </p>
          <Link to="/app/agents" className="text-xs text-ember hover:underline">
            Agents →
          </Link>
        </GlassPanel>
      </div>
    </div>
  )
}

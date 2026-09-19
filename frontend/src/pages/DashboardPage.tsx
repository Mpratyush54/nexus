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
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { Button } from '@/components/ui/Button'
import { useToast } from '@/components/ui/Toast'
import { useDashboard } from '@/hooks/useDashboard'
import { useMemorySearch } from '@/hooks/useMemory'
import {
  useLocalDaemonAutodetect,
  useLocalHarvest,
  useLocalWorkspaceView,
  useTriggerHarvestScan,
} from '@/hooks/useDaemon'
import { useCurrentProject } from '@/hooks/useProjects'
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

export function DashboardPage() {
  const { projectId } = useAuth()
  const { current } = useCurrentProject()
  const dash = useDashboard()
  const memories = useMemorySearch('')
  const local = useLocalDaemonAutodetect()
  const serverView = useLocalWorkspaceView()
  const bridgeUrl =
    local.data?.baseUrl ||
    local.data?.status.proxy_url ||
    serverView.data?.proxy_url ||
    undefined
  const harvest = useLocalHarvest(bridgeUrl)
  const scanNow = useTriggerHarvestScan(bridgeUrl)
  const { push } = useToast()

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

  const memoryItems = memories.data ?? []
  const proposed = memoryItems.filter((x) => x.status === 'PROPOSED').slice(0, 6)
  const confirmed = memoryItems.filter((x) => x.status === 'CONFIRMED').slice(0, 6)
  const shown = proposed.length ? proposed : confirmed

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
            Memories for this project · harvest scans your machine into this project’s review queue
          </p>
        </div>
        <div className="w-full max-w-xs sm:w-56">
          <ProjectSwitcher compact />
        </div>
      </div>

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
              <span className="font-mono text-fg">{hs?.root || local.data?.status.root || '—'}</span>
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
                void local.refetch()
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

import { useQuery } from '@tanstack/react-query'
import { History, Laptop, Layers, RefreshCw, ScanSearch, Search, Smartphone, X } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { chatsApi } from '@/api/chats'
import type { ActiveWorkspace } from '@/api/team'
import { ActivityHeatmap } from '@/components/ActivityHeatmap'
import { Advanced } from '@/components/Advanced'
import { PageHeader } from '@/components/PageHeader'
import { ProjectSwitcher } from '@/components/ProjectSwitcher'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { useToast } from '@/components/ui/Toast'
import { useDashboard } from '@/hooks/useDashboard'
import { useLocalHarvest, useTriggerHarvestScan } from '@/hooks/useDaemon'
import { useFollowHarvestProject } from '@/hooks/useFollowHarvestProject'
import { useMemorySearch } from '@/hooks/useMemory'
import { useCurrentProject } from '@/hooks/useProjects'
import { useProjectSummaries } from '@/hooks/useProjectSummaries'
import { useActiveWorkspaces } from '@/hooks/useTeam'
import { useTrustedLocalBridge } from '@/hooks/useTrustedLocalBridge'
import { readableChatText } from '@/lib/chat-content'
import { agentName, chatTitle } from '@/lib/harness-message'
import { isActiveMemory } from '@/lib/memory-status'
import { memoryTitle } from '@/lib/plain-language'
import { projectLabel } from '@/lib/project-label'
import { STEER_ROUTE } from '@/lib/navigation'
import { useAuth } from '@/providers/AuthProvider'
import { formatRelative } from '@/utils/format'
import { readPref, writePref } from '@/utils/prefs'
import { storage } from '@/utils/storage'

const HINT_KEY = 'nexus.home.hint.dismissed'

const THINGS = [
  {
    icon: History,
    title: 'Find a past chat',
    body: 'Search everything you and your agents talked about.',
    to: '/app/chats',
  },
  {
    icon: Layers,
    title: 'See what your agents learned',
    body: 'Decisions and facts saved from your chats, so the next agent knows them.',
    to: '/app/memory',
  },
  {
    icon: Smartphone,
    title: 'Reply to an agent from your phone',
    body: 'Open a chat running on your computer and reply from anywhere.',
    to: STEER_ROUTE,
  },
]

/**
 * Home: one search box (the main action), what is new, and whether a computer
 * is saving chats. Counts, the activity heatmap and scanner diagnostics are
 * under Details.
 */
export function DashboardPage() {
  const { projectId, setProjectId } = useAuth()
  const navigate = useNavigate()
  const { current } = useCurrentProject()
  const summaries = useProjectSummaries()
  const workspaces = useActiveWorkspaces()
  const memories = useMemorySearch('')
  const [hintOpen, setHintOpen] = useState(() => readPref(HINT_KEY) !== '1')
  const [q, setQ] = useState('')
  const chats = useQuery({
    queryKey: ['home-chats', projectId],
    enabled: Boolean(projectId),
    queryFn: ({ signal }) => chatsApi.list(projectId!, '', '', signal),
    select: (page) => page.items.slice(0, 5),
  })

  // Desktop on this computer may be saving to a different project.
  const bridge = useTrustedLocalBridge()
  const harvest = useLocalHarvest(bridge.bridgeUrl)
  useFollowHarvestProject(harvest.data, bridge.trusted)
  const hs = harvest.data
  const mismatch = Boolean(hs?.project_id && projectId && hs.project_id !== projectId)
  const mismatchRow = (summaries.data ?? []).find((s) => s.project.id === hs?.project_id)

  const online = ((workspaces.data ?? []) as ActiveWorkspace[]).filter((w) => w.is_online !== false)
  const recentMemories = (memories.data ?? []).filter((m) => isActiveMemory(m.status)).slice(0, 5)

  const onSearch = (e: FormEvent) => {
    e.preventDefault()
    const term = q.trim()
    navigate(term ? `/app/chats?q=${encodeURIComponent(term)}` : '/app/chats')
  }

  if (!projectId) {
    return (
      <div className="space-y-6 py-8">
        <PageHeader title="Home" description="Pick a project to see its chats and memory." />
        <ProjectSwitcher />
        <Link to="/app/onboarding" className="text-sm text-amber hover:underline">
          Set up the desktop app →
        </Link>
      </div>
    )
  }

  return (
    <div className="space-y-8">
      <PageHeader
        title="Home"
        description={
          <>
            Your AI coding chats and what your agents learned in{' '}
            <span className="font-medium text-fg">{current ? projectLabel(current) : 'this project'}</span>.
          </>
        }
      />

      <form onSubmit={onSearch} className="flex gap-2">
        <label className="relative min-w-0 flex-1">
          <span className="sr-only">Search your chats</span>
          <Search size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-muted" aria-hidden />
          <input
            type="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search your chats, e.g. “retry backoff”"
            className="h-11 w-full rounded-lg border border-border bg-raised pl-9 pr-3 text-sm text-fg outline-none focus:border-amber"
          />
        </label>
        <Button type="submit">Search</Button>
      </form>

      {hintOpen ? (
        <GlassPanel className="relative p-5">
          <div className="flex items-start justify-between gap-3">
            <h2 className="text-sm font-semibold text-fg">3 things you can do here</h2>
            <button
              type="button"
              className="-m-1 rounded p-1 text-muted hover:text-fg"
              aria-label="Dismiss tips"
              onClick={() => {
                setHintOpen(false)
                writePref(HINT_KEY, '1')
              }}
            >
              <X size={16} />
            </button>
          </div>
          <ol className="mt-3 grid gap-3 sm:grid-cols-3">
            {THINGS.map((t, i) => (
              <li key={t.to}>
                <Link to={t.to} className="block h-full rounded-lg border border-border p-3 transition hover:border-border-strong">
                  <span className="flex items-center gap-2 text-sm font-medium text-fg">
                    <span className="flex h-5 w-5 items-center justify-center rounded-full bg-raised text-[11px] text-fg-dim">{i + 1}</span>
                    {t.title}
                  </span>
                  <span className="mt-1 block text-xs text-fg-dim">{t.body}</span>
                </Link>
              </li>
            ))}
          </ol>
        </GlassPanel>
      ) : null}

      {mismatch && hs?.project_id ? (
        <GlassPanel className="flex flex-wrap items-center justify-between gap-3 border border-amber/40 p-4">
          <p className="text-sm text-fg">
            The desktop app on this computer is saving chats to{' '}
            <span className="font-medium">{mismatchRow ? projectLabel(mismatchRow.project) : 'another project'}</span>.
          </p>
          <Button type="button" size="sm" onClick={() => setProjectId(hs.project_id!)}>
            Show that project
          </Button>
        </GlassPanel>
      ) : null}

      <p className="flex items-center gap-2 text-sm text-fg-dim">
        <Laptop size={16} className="shrink-0" aria-hidden />
        {workspaces.isLoading ? (
          'Checking your computers…'
        ) : online.length ? (
          <span>
            <span className="text-fg">{online.map((w) => w.machine_id || 'A computer').join(', ')}</span>{' '}
            {online.length === 1 ? 'is' : 'are'} online and saving chats.
          </span>
        ) : (
          <span>
            No computer is saving chats yet.{' '}
            <Link to="/app/connect" className="text-amber hover:underline">
              Set up the desktop app
            </Link>
          </span>
        )}
      </p>

      <div className="grid gap-6 lg:grid-cols-2">
        <GlassPanel className="p-5">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-semibold text-fg">Recent chats</h2>
            <Link to="/app/chats" className="text-xs text-ember hover:underline">
              All chats →
            </Link>
          </div>
          <ul className="mt-3 divide-y divide-border">
            {(chats.data ?? []).map((c) => (
              <li key={c.session_id}>
                <Link to={`/app/chats?chat=${encodeURIComponent(c.session_id)}`} className="block py-2.5 hover:text-fg">
                  <span className="block truncate text-sm text-fg">{chatTitle(c.title, c.summary, c.harness)}</span>
                  <span className="block text-xs text-muted">
                    {agentName(c.harness)} · {formatRelative(c.last_active_at)}
                  </span>
                </Link>
              </li>
            ))}
            {chats.isLoading ? <li className="py-3 text-sm text-muted">Loading…</li> : null}
            {!chats.isLoading && !(chats.data ?? []).length ? (
              <li className="py-3 text-sm text-fg-dim">
                No chats yet. Install the desktop app and chat with Claude Code, Codex or Cursor — your chats show up here.
              </li>
            ) : null}
          </ul>
        </GlassPanel>

        <GlassPanel className="p-5">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-semibold text-fg">What your agents learned</h2>
            <Link to="/app/memory" className="text-xs text-ember hover:underline">
              All memories →
            </Link>
          </div>
          <ul className="mt-3 divide-y divide-border">
            {recentMemories.map((m) => (
              <li key={m.id} className="py-2.5">
                <span className="block truncate text-sm text-fg">{memoryTitle(m.key).title}</span>
                <span className="mt-0.5 line-clamp-2 text-xs text-fg-dim">{readableChatText(m.content).replace(/(^|\s)#{1,6}\s+/g, ' ').trim()}</span>
              </li>
            ))}
            {memories.isLoading ? <li className="py-3 text-sm text-muted">Loading…</li> : null}
            {!memories.isLoading && !recentMemories.length ? (
              <li className="py-3 text-sm text-fg-dim">
                Nothing yet. When an agent figures something out in a chat, it’s saved here for next time.
              </li>
            ) : null}
          </ul>
        </GlassPanel>
      </div>

      {!storage.getOnboardingDone() ? (
        <p className="text-sm text-fg-dim">
          New here?{' '}
          <Link to="/app/onboarding" className="text-amber hover:underline">
            Set up the desktop app
          </Link>{' '}
          so your agent chats are saved.
        </p>
      ) : null}

      <Advanced title="Details" summary="Counts, a year of activity, and the desktop scanner on this computer.">
        <DashboardDetails />
      </Advanced>
    </div>
  )
}

function DashboardDetails() {
  const { projectId } = useAuth()
  const dash = useDashboard()
  const bridge = useTrustedLocalBridge()
  const harvest = useLocalHarvest(bridge.bridgeUrl)
  const scanNow = useTriggerHarvestScan(bridge.bridgeUrl)
  const { push } = useToast()
  const m = dash.data?.metrics
  const hs = harvest.data
  const stats: [string, string | number, string][] = [
    ['Memories', m?.memories ?? '—', m ? `${m.proposed} new` : ''],
    ['People', m?.members ?? '—', m ? `${m.online} online` : ''],
    ['Agent lookups', m?.agent_calls ?? '—', 'Times an agent read memory'],
    ['Saved by this computer', hs?.proposals_saved ?? 0, hs?.running ? `Last scan ${hs.last_scan_at ? formatRelative(hs.last_scan_at) : '—'}` : 'Desktop app not running here'],
  ]
  return (
    <div className="space-y-6">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {stats.map(([label, value, sub]) => (
          <div key={label} className="rounded-lg border border-border p-3">
            <p className="text-xs text-muted">{label}</p>
            <p className="mt-1 text-xl font-semibold text-fg">{value}</p>
            <p className="mt-0.5 text-xs text-fg-dim">{sub}</p>
          </div>
        ))}
      </div>
      <div className="space-y-2">
        <p className="text-sm font-medium text-fg">Agent messages over the last year</p>
        <ActivityHeatmap days={dash.data?.heatmap} loading={dash.isLoading} />
      </div>
      <div className="space-y-2">
        <p className="text-sm font-medium text-fg">Desktop scanner on this computer</p>
        <p className="text-xs text-fg-dim">
          {hs?.message || 'Start the desktop app on this computer to see its scans here.'}
        </p>
        <dl className="grid gap-1 text-xs text-fg-dim sm:grid-cols-2">
          <div><dt className="inline text-muted">Project ID </dt><dd className="inline font-mono">{projectId}</dd></div>
          <div><dt className="inline text-muted">Scanner project ID </dt><dd className="inline font-mono">{hs?.project_id || '—'}</dd></div>
          <div><dt className="inline text-muted">Folder </dt><dd className="inline font-mono">{hs?.root || bridge.local.data?.status.root || '—'}</dd></div>
          <div><dt className="inline text-muted">Last scan </dt><dd className="inline">{hs?.last_scan_files ?? 0} files · {hs?.last_scan_turns ?? 0} messages</dd></div>
        </dl>
        <div className="flex flex-wrap gap-2 pt-1">
          <Button
            type="button"
            size="sm"
            variant="secondary"
            disabled={!bridge.bridgeUrl || scanNow.isPending}
            onClick={() =>
              scanNow.mutate(undefined, {
                onSuccess: (st) => push({ title: 'Scan complete', detail: st.message || `${st.last_scan_files ?? 0} files`, tone: 'teal' }),
                onError: (err) => push({ title: 'Scan failed', detail: err instanceof Error ? err.message : 'Desktop app not running', tone: 'danger' }),
              })
            }
          >
            <ScanSearch size={14} />
            {scanNow.isPending ? 'Scanning…' : 'Scan now'}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={() => {
              void harvest.refetch()
              void bridge.local.refetch()
            }}
          >
            <RefreshCw size={14} />
            Refresh
          </Button>
        </div>
      </div>
    </div>
  )
}

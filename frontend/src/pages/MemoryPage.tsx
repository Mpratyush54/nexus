import { AnimatePresence, motion } from 'framer-motion'
import {
  BookmarkCheck,
  Bot,
  Calendar,
  ChevronDown,
  ChevronRight,
  Code2,
  FileCode2,
  GitBranch,
  History,
  Layers,
  Package,
  Pencil,
  RefreshCw,
  Server,
  Share2,
  ShieldCheck,
  Sparkles,
  Terminal,
} from 'lucide-react'
import { useDeferredValue, useMemo, useState } from 'react'
import { MemoryEditorSlideOver } from '@/components/MemoryEditorSlideOver'
import { SharingControlsModal } from '@/components/SharingControlsModal'
import { VersionHistoryDrawer } from '@/components/VersionHistoryDrawer'
import { ProjectSwitcher } from '@/components/ProjectSwitcher'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import { memoryApi, type HarvestJob } from '@/api/memory'
import { useConfirmMemory, useHarvestQueue, useMemorySearch, useRejectMemory } from '@/hooks/useMemory'
import { useLocalHarvest } from '@/hooks/useDaemon'
import { useFollowHarvestProject } from '@/hooks/useFollowHarvestProject'
import { useTrustedLocalBridge } from '@/hooks/useTrustedLocalBridge'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError, type MemoryItem } from '@/types/api'
import { formatRelative } from '@/utils/format'
import { useProjectSummaries } from '@/hooks/useProjectSummaries'
import { useProjects } from '@/hooks/useProjects'

const LEVEL_FILTERS = ['', 'project', 'personal', 'organization', 'session'] as const

const CATEGORY_ORDER = [
  'architecture',
  'infrastructure',
  'auth',
  'api',
  'conventions',
  'dependencies',
  'general',
] as const

const CATEGORY_CONFIG: Record<
  string,
  { label: string; icon: typeof Layers; badgeClass: string; desc: string }
> = {
  architecture: {
    label: 'Architecture',
    icon: Layers,
    badgeClass: 'text-sky-400 border-sky-500/30 bg-sky-500/10',
    desc: 'System design, module boundaries, databases, schemas, core flow',
  },
  infrastructure: {
    label: 'Infrastructure',
    icon: Server,
    badgeClass: 'text-purple-400 border-purple-500/30 bg-purple-500/10',
    desc: 'Docker, AWS, Lightsail, CI/CD, systemd, hosting, ports',
  },
  auth: {
    label: 'Authentication',
    icon: ShieldCheck,
    badgeClass: 'text-amber-400 border-amber-500/30 bg-amber-500/10',
    desc: 'Credentials, tokens, secrets, SMTP, OAuth, permissions',
  },
  api: {
    label: 'API & Schemas',
    icon: Code2,
    badgeClass: 'text-emerald-400 border-emerald-500/30 bg-emerald-500/10',
    desc: 'HTTP endpoints, payloads, RPC, schemas, protocols',
  },
  conventions: {
    label: 'Conventions',
    icon: BookmarkCheck,
    badgeClass: 'text-teal-400 border-teal-500/30 bg-teal-500/10',
    desc: 'Coding style, linting, directory layout, naming, project patterns',
  },
  dependencies: {
    label: 'Dependencies',
    icon: Package,
    badgeClass: 'text-indigo-400 border-indigo-500/30 bg-indigo-500/10',
    desc: 'Libraries, versions, package managers, SDKs, external services',
  },
  general: {
    label: 'General Facts',
    icon: Sparkles,
    badgeClass: 'text-zinc-400 border-zinc-500/30 bg-zinc-500/10',
    desc: 'General codebase facts, notes, and session summaries',
  },
}

function projectLabelOf(p: { display_name?: string; folder_name?: string; id: string }) {
  return p.display_name || p.folder_name || `${p.id.slice(0, 8)}…`
}

function normalizeItemCategory(cat?: string): string {
  if (!cat) return 'general'
  const c = cat.toLowerCase().trim()
  if (c in CATEGORY_CONFIG) return c
  return 'general'
}

function formatWeekLabel(bucket?: string, dateStr?: string): string {
  if (bucket && bucket.includes('-W')) {
    const parts = bucket.split('-W')
    return `Week ${parts[1]}, ${parts[0]}`
  }
  if (dateStr) {
    const d = new Date(dateStr)
    if (!isNaN(d.getTime())) {
      return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
    }
  }
  return 'Recent Changes'
}

export function MemoryPage() {
  const { projectId, setProjectId } = useAuth()
  const { push } = useToast()
  const projects = useProjects()
  const summaries = useProjectSummaries()
  const bridge = useTrustedLocalBridge()
  const harvestStatus = useLocalHarvest(bridge.bridgeUrl).data
  useFollowHarvestProject(harvestStatus, bridge.trusted)

  const [viewMode, setViewMode] = useState<'hierarchy' | 'timeline'>('hierarchy')
  const [q, setQ] = useState('')
  const [level, setLevel] = useState('')
  const [selectedCategory, setSelectedCategory] = useState<string | null>(null)
  const [tagFilter, setTagFilter] = useState<string | null>(null)
  const deferredQ = useDeferredValue(q)
  const search = useMemorySearch(deferredQ, level)
  const harvestQ = useHarvestQueue()
  const confirm = useConfirmMemory()
  const reject = useRejectMemory()

  const [expandedHarvest, setExpandedHarvest] = useState<string | null>(null)
  const [harvestFull, setHarvestFull] = useState<Record<string, HarvestJob>>({})
  const [harvestLoading, setHarvestLoading] = useState<string | null>(null)
  const [collapsedCategories, setCollapsedCategories] = useState<Record<string, boolean>>({})

  const toggleHarvest = async (id: string) => {
    if (expandedHarvest === id) {
      setExpandedHarvest(null)
      return
    }
    setExpandedHarvest(id)
    if (harvestFull[id]) return
    setHarvestLoading(id)
    try {
      const job = await memoryApi.harvestJob(id)
      setHarvestFull((prev) => ({ ...prev, [id]: job }))
    } catch (err) {
      push({
        title: 'Could not load full harvest',
        detail: err instanceof Error ? err.message : 'Request failed',
        tone: 'danger',
      })
    } finally {
      setHarvestLoading(null)
    }
  }

  const toggleCategory = (cat: string) => {
    setCollapsedCategories((prev) => ({ ...prev, [cat]: !prev[cat] }))
  }

  const [editing, setEditing] = useState<MemoryItem | null>(null)
  const [historyItem, setHistoryItem] = useState<MemoryItem | null>(null)
  const [shareItem, setShareItem] = useState<MemoryItem | null>(null)

  const items = search.data ?? []
  const allTags = useMemo(() => {
    const set = new Set<string>()
    for (const item of items) {
      for (const tag of item.tags ?? []) set.add(tag)
    }
    return [...set].sort()
  }, [items])

  const filtered = useMemo(() => {
    return items.filter((i) => {
      if (tagFilter && !(i.tags ?? []).includes(tagFilter)) return false
      if (selectedCategory && normalizeItemCategory(i.category) !== selectedCategory) return false
      return true
    })
  }, [items, tagFilter, selectedCategory])

  const proposed = useMemo(() => filtered.filter((i) => i.status === 'PROPOSED'), [filtered])
  const confirmed = useMemo(() => filtered.filter((i) => i.status !== 'PROPOSED'), [filtered])

  // Group confirmed items by category for Hierarchy View
  const itemsByCategory = useMemo(() => {
    const map: Record<string, MemoryItem[]> = {}
    for (const cat of CATEGORY_ORDER) {
      map[cat] = []
    }
    for (const item of confirmed) {
      const c = normalizeItemCategory(item.category)
      if (!map[c]) map[c] = []
      map[c].push(item)
    }
    return map
  }, [confirmed])

  // Group confirmed items by week bucket for Timeline View
  const itemsByWeek = useMemo(() => {
    const map: Record<string, MemoryItem[]> = {}
    for (const item of confirmed) {
      const bucket = item.week_bucket?.trim() || 'Current'
      if (!map[bucket]) map[bucket] = []
      map[bucket].push(item)
    }
    const sortedKeys = Object.keys(map).sort((a, b) => b.localeCompare(a))
    return sortedKeys.map((key) => ({
      bucket: key,
      label: formatWeekLabel(key, map[key][0]?.created_at),
      items: map[key],
    }))
  }, [confirmed])

  // Only in-flight batches — done/failed leave this list; facts land in Library.
  const incomingHarvest = useMemo(
    () => (harvestQ.data?.items ?? []).filter((j) => j.status === 'queued' || j.status === 'processing'),
    [harvestQ.data],
  )
  const harvestInFlight = harvestQ.data?.inFlight ?? incomingHarvest.length
  const harvestQueued = harvestQ.data?.counts?.queued ?? incomingHarvest.filter((j) => j.status === 'queued').length
  const harvestProcessing =
    harvestQ.data?.counts?.processing ?? incomingHarvest.filter((j) => j.status === 'processing').length

  const onConfirm = (id: string, key: string) => {
    confirm.mutate(id, {
      onSuccess: () => push({ title: 'Confirmed', detail: key, tone: 'teal' }),
      onError: (err) =>
        push({
          title: 'Confirm failed',
          detail: err instanceof ApiError ? err.message : 'Unknown error',
          tone: 'danger',
        }),
      })
  }

  const onReject = (id: string, key: string) => {
    reject.mutate(id, {
      onSuccess: () => push({ title: 'Rejected', detail: key }),
      onError: (err) =>
        push({
          title: 'Reject failed',
          detail: err instanceof ApiError ? err.message : 'Unknown error',
          tone: 'danger',
        }),
    })
  }

  const harvestTargetId = harvestStatus?.project_id?.trim()
  const harvestMismatch = Boolean(harvestTargetId && projectId && harvestTargetId !== projectId)
  const harvestProject = (projects.data ?? []).find((p) => p.id === harvestTargetId)
  const richer = (summaries.data ?? []).find(
    (s) =>
      s.project.id !== projectId &&
      s.metrics.memories > (filtered.length + 5),
  )

  return (
    <div className="space-y-8">
      {/* Top Header */}
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-fg">Project Memory & Knowledge</h1>
          <p className="mt-1 max-w-xl text-sm text-fg-dim">
            Hierarchical architecture, state of truth, and chronological evolution harvested from agent pair programming.
            {!projectId ? ' Select a project to load memories.' : null}
          </p>
          <div className="mt-3 max-w-xs">
            <ProjectSwitcher compact />
          </div>
        </div>
        <div className="flex items-center gap-2">
          {/* Tab Selector */}
          <div className="flex rounded-lg border border-border bg-raised/70 p-0.5">
            <button
              type="button"
              onClick={() => setViewMode('hierarchy')}
              className={[
                'flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition',
                viewMode === 'hierarchy'
                  ? 'bg-amber text-black shadow-sm font-semibold'
                  : 'text-fg-dim hover:text-fg',
              ].join(' ')}
            >
              <Layers size={13} />
              Active Truth
            </button>
            <button
              type="button"
              onClick={() => setViewMode('timeline')}
              className={[
                'flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition',
                viewMode === 'timeline'
                  ? 'bg-amber text-black shadow-sm font-semibold'
                  : 'text-fg-dim hover:text-fg',
              ].join(' ')}
            >
              <Calendar size={13} />
              Timeline & Changelog
            </button>
          </div>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void search.refetch()}
            disabled={search.isFetching}
          >
            <RefreshCw size={13} className={search.isFetching ? 'animate-spin' : ''} />
            Refresh
          </Button>
        </div>
      </div>

      {harvestMismatch && harvestTargetId ? (
        <GlassPanel className="flex flex-wrap items-center justify-between gap-3 border border-amber/40 p-4">
          <div>
            <p className="text-sm text-fg">
              Desktop is writing memories to{' '}
              <span className="font-medium">
                {harvestProject ? projectLabelOf(harvestProject) : 'another project'}
              </span>
              , not this Library.
            </p>
            <p className="mt-1 text-xs text-fg-dim">
              Folder <span className="font-mono">central-memory</span> maps to git remote{' '}
              <span className="font-mono">nexus</span>.
            </p>
          </div>
          <Button type="button" size="sm" onClick={() => setProjectId(harvestTargetId)}>
            Show harvest project
          </Button>
        </GlassPanel>
      ) : null}

      {richer && !harvestMismatch ? (
        <GlassPanel className="flex flex-wrap items-center justify-between gap-3 border border-amber/30 p-4">
          <div>
            <p className="text-sm text-fg">
              <span className="font-medium">{projectLabelOf(richer.project)}</span> has{' '}
              {richer.metrics.memories} memories — more than this project.
            </p>
          </div>
          <Button type="button" size="sm" onClick={() => setProjectId(richer.project.id)}>
            Switch to {projectLabelOf(richer.project)}
          </Button>
        </GlassPanel>
      ) : null}

      {/* In-flight Harvest Queue */}
      {harvestInFlight > 0 ? (
        <GlassPanel className="space-y-3 p-5">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-medium text-fg">Incoming harvest (raw batches)</h2>
            <StatusPill tone="amber">
              {`${harvestQueued} queued · ${harvestProcessing} processing${
                harvestInFlight > incomingHarvest.length
                  ? ` · ${harvestInFlight} total in flight`
                  : ''
              }`}
            </StatusPill>
          </div>
          <p className="text-[11px] text-muted">
            OpenRouter is extracting architecture facts and decisions. Processed items appear automatically.
            {harvestInFlight > incomingHarvest.length
              ? ` Showing ${incomingHarvest.length} of ${harvestInFlight} in-flight batches.`
              : null}
          </p>
          <ul className="max-h-[28rem] space-y-2 overflow-auto text-sm">
            {incomingHarvest.slice(0, 20).map((job) => {
              const open = expandedHarvest === job.id
              const full = harvestFull[job.id]
              const body =
                open && full?.turns?.length
                  ? full.turns
                      .map((t) => `${t.speaker || '?'}: ${t.content}`)
                      .join('\n\n')
                  : job.raw_preview || '(empty)'
              return (
                <li key={job.id} className="rounded-lg border border-border bg-raised/40 px-3 py-2">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <StatusPill tone="amber">{job.status}</StatusPill>
                      <span className="text-[11px] text-muted">
                        {job.turn_count} turns
                        {job.result_count ? ` · ${job.result_count} memories` : ''}
                        {job.provider ? ` · ${job.provider}` : ''}
                      </span>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      onClick={() => void toggleHarvest(job.id)}
                    >
                      {harvestLoading === job.id
                        ? 'Loading…'
                        : open
                          ? 'Collapse'
                          : 'Show full'}
                    </Button>
                  </div>
                  <pre className="mt-1.5 max-h-80 overflow-auto whitespace-pre-wrap font-sans text-xs text-fg-dim">
                    {body}
                  </pre>
                  {job.error ? <p className="mt-1 text-[11px] text-danger">{job.error}</p> : null}
                </li>
              )
            })}
          </ul>
        </GlassPanel>
      ) : null}

      {/* Search & Filter Bar */}
      <div className="space-y-3">
        <div className="flex flex-wrap items-end gap-3">
          <label className="block min-w-[12rem] flex-1 max-w-md">
            <span className="mb-1.5 block text-xs text-fg-dim">Search memories, files, commands</span>
            <input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="e.g. redis, jwt, docker, lightsail, store.go..."
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block text-xs text-fg-dim">Level</span>
            <select
              value={level}
              onChange={(e) => setLevel(e.target.value)}
              className="h-10 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
            >
              {LEVEL_FILTERS.map((l) => (
                <option key={l || 'all'} value={l}>
                  {l || 'All levels'}
                </option>
              ))}
            </select>
          </label>
        </div>

        {/* Category Pills */}
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-muted mr-1">Category:</span>
          <button
            type="button"
            onClick={() => setSelectedCategory(null)}
            className={[
              'rounded-md px-2.5 py-1 text-xs font-medium transition',
              selectedCategory === null
                ? 'bg-amber-soft text-amber'
                : 'text-muted hover:bg-raised hover:text-fg',
            ].join(' ')}
          >
            All
          </button>
          {CATEGORY_ORDER.map((catKey) => {
            const cfg = CATEGORY_CONFIG[catKey]
            const active = selectedCategory === catKey
            const Icon = cfg.icon
            return (
              <button
                key={catKey}
                type="button"
                onClick={() => setSelectedCategory((cur) => (cur === catKey ? null : catKey))}
                className={[
                  'flex items-center gap-1 rounded-md px-2.5 py-1 text-xs font-medium transition border',
                  active
                    ? 'bg-raised border-amber text-amber font-semibold'
                    : 'border-border/50 text-muted hover:bg-raised hover:text-fg',
                ].join(' ')}
              >
                <Icon size={12} />
                {cfg.label}
              </button>
            )
          })}
        </div>

        {/* Tag Pills */}
        {allTags.length > 0 ? (
          <div className="flex flex-wrap items-center gap-1.5 pt-1">
            <span className="text-xs text-muted mr-1">Tags:</span>
            <button
              type="button"
              onClick={() => setTagFilter(null)}
              className={[
                'rounded-md px-2 py-0.5 font-mono text-[11px] transition',
                tagFilter === null
                  ? 'bg-amber-soft text-amber'
                  : 'text-muted hover:bg-raised hover:text-fg',
              ].join(' ')}
            >
              all
            </button>
            {allTags.slice(0, 15).map((tag) => (
              <button
                key={tag}
                type="button"
                onClick={() => setTagFilter((cur) => (cur === tag ? null : tag))}
                className={[
                  'rounded-md px-2 py-0.5 font-mono text-[11px] transition',
                  tagFilter === tag
                    ? 'bg-amber-soft text-amber'
                    : 'text-muted hover:bg-raised hover:text-fg',
                ].join(' ')}
              >
                #{tag}
              </button>
            ))}
          </div>
        ) : null}
      </div>

      {search.isLoading ? <p className="text-sm text-muted">Loading memories…</p> : null}

      {search.isError ? (
        <GlassPanel className="p-4">
          <p className="text-sm text-danger">
            {search.error instanceof ApiError
              ? search.error.message
              : 'Failed to load memories'}
          </p>
        </GlassPanel>
      ) : null}

      {!search.isLoading && !search.isError && filtered.length === 0 ? (
        <GlassPanel className="p-6">
          <p className="text-sm text-fg-dim">
            No memories found. Run an agent conversation or harvest from desktop to record decisions.
          </p>
        </GlassPanel>
      ) : null}

      {/* Review Queue (Proposed Items) */}
      {proposed.length > 0 ? (
        <section className="space-y-2.5">
          <h2 className="text-xs font-medium uppercase tracking-wide text-amber flex items-center gap-2">
            <span>Review queue</span>
            <span className="rounded-full bg-amber/20 px-2 py-0.5 text-[10px] font-bold text-amber">
              {proposed.length}
            </span>
          </h2>
          <AnimatePresence initial={false}>
            {proposed.map((item) => (
              <MemoryCard
                key={item.id}
                item={item}
                busy={confirm.isPending || reject.isPending}
                onConfirm={() => onConfirm(item.id, item.key)}
                onReject={() => onReject(item.id, item.key)}
                onEdit={() => setEditing(item)}
                onHistory={() => setHistoryItem(item)}
                onShare={() => setShareItem(item)}
              />
            ))}
          </AnimatePresence>
        </section>
      ) : null}

      {/* MAIN VIEW: HIERARCHY (ACTIVE TRUTH) */}
      {viewMode === 'hierarchy' && confirmed.length > 0 ? (
        <div className="space-y-6">
          <div className="flex items-center justify-between">
            <h2 className="text-xs font-medium uppercase tracking-wide text-muted">
              Active Project Architecture & Truth · {confirmed.length} active records
            </h2>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="secondary"
                size="sm"
                disabled={!search.hasPrev || search.isFetching}
                onClick={() => search.prevPage()}
              >
                Previous
              </Button>
              <span className="text-[11px] tabular-nums text-muted">
                {search.page + 1} / {search.pageCount}
              </span>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                disabled={!search.hasNext || search.isFetching}
                onClick={() => search.nextPage()}
              >
                Next
              </Button>
            </div>
          </div>

          {CATEGORY_ORDER.map((catKey) => {
            const catItems = itemsByCategory[catKey] ?? []
            if (catItems.length === 0) return null
            const cfg = CATEGORY_CONFIG[catKey]
            const Icon = cfg.icon
            const isCollapsed = collapsedCategories[catKey] ?? false

            return (
              <div key={catKey} className="rounded-xl border border-border/70 bg-surface/50 overflow-hidden">
                <button
                  type="button"
                  onClick={() => toggleCategory(catKey)}
                  className="w-full flex items-center justify-between p-3.5 bg-raised/30 hover:bg-raised/60 transition text-left"
                >
                  <div className="flex items-center gap-2.5">
                    <span className={`p-1.5 rounded-lg border ${cfg.badgeClass}`}>
                      <Icon size={16} />
                    </span>
                    <div>
                      <div className="flex items-center gap-2">
                        <span className="font-semibold text-sm text-fg">{cfg.label}</span>
                        <span className="text-xs text-muted font-mono">({catItems.length})</span>
                      </div>
                      <p className="text-[11px] text-muted">{cfg.desc}</p>
                    </div>
                  </div>
                  <span className="text-muted hover:text-fg p-1">
                    {isCollapsed ? <ChevronRight size={16} /> : <ChevronDown size={16} />}
                  </span>
                </button>

                {!isCollapsed && (
                  <div className="p-3 space-y-2.5">
                    <AnimatePresence initial={false}>
                      {catItems.map((item) => (
                        <MemoryCard
                          key={item.id}
                          item={item}
                          onEdit={() => setEditing(item)}
                          onHistory={() => setHistoryItem(item)}
                          onShare={() => setShareItem(item)}
                        />
                      ))}
                    </AnimatePresence>
                  </div>
                )}
              </div>
            )
          })}
        </div>
      ) : null}

      {/* MAIN VIEW: TIMELINE & CHANGELOG */}
      {viewMode === 'timeline' && confirmed.length > 0 ? (
        <div className="space-y-8">
          <div className="flex items-center justify-between">
            <h2 className="text-xs font-medium uppercase tracking-wide text-muted">
              Project Evolution & Changelog by Week · {confirmed.length} records
            </h2>
          </div>

          <div className="relative pl-6 space-y-8 before:absolute before:bottom-0 before:left-2 before:top-2 before:w-[2px] before:bg-border">
            {itemsByWeek.map((week) => (
              <div key={week.bucket} className="relative space-y-3">
                <div className="flex items-center gap-2">
                  <div className="absolute -left-6 flex h-4 w-4 items-center justify-center rounded-full bg-amber ring-4 ring-bg" />
                  <h3 className="text-sm font-semibold text-fg flex items-center gap-2">
                    <Calendar size={14} className="text-amber" />
                    <span>{week.label}</span>
                    <span className="text-xs text-muted font-mono font-normal">
                      ({week.items.length} {week.items.length === 1 ? 'event' : 'events'})
                    </span>
                  </h3>
                </div>

                <div className="space-y-2.5">
                  {week.items.map((item) => (
                    <MemoryCard
                      key={item.id}
                      item={item}
                      onEdit={() => setEditing(item)}
                      onHistory={() => setHistoryItem(item)}
                      onShare={() => setShareItem(item)}
                    />
                  ))}
                </div>
              </div>
            ))}
          </div>
        </div>
      ) : null}

      <MemoryEditorSlideOver
        item={editing}
        open={Boolean(editing)}
        onClose={() => setEditing(null)}
        onOpenHistory={() => {
          if (editing) setHistoryItem(editing)
        }}
        onOpenShare={() => {
          if (editing) setShareItem(editing)
        }}
      />
      <VersionHistoryDrawer
        item={historyItem}
        open={Boolean(historyItem)}
        onClose={() => setHistoryItem(null)}
      />
      <SharingControlsModal
        item={shareItem}
        open={Boolean(shareItem)}
        onClose={() => setShareItem(null)}
      />
    </div>
  )
}

function MemoryCard({
  item,
  onConfirm,
  onReject,
  onEdit,
  onHistory,
  onShare,
  busy,
}: {
  item: MemoryItem
  onConfirm?: () => void
  onReject?: () => void
  onEdit?: () => void
  onHistory?: () => void
  onShare?: () => void
  busy?: boolean
}) {
  const [expanded, setExpanded] = useState(item.scope === 'episode_summary')
  const proposed = item.status === 'PROPOSED'
  const agent = item.proposed_by || item.source
  const previewLimit = item.scope === 'episode_summary' ? 420 : 180
  const preview =
    item.content.length > previewLimit && !expanded
      ? `${item.content.slice(0, previewLimit).trimEnd()}…`
      : item.content

  const catKey = normalizeItemCategory(item.category)
  const catConfig = CATEGORY_CONFIG[catKey]
  const CatIcon = catConfig.icon

  return (
    <motion.div
      layout
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, x: proposed ? 48 : -24, height: 0, marginBottom: 0 }}
      transition={{ type: 'spring', stiffness: 380, damping: 32 }}
      className="group overflow-hidden"
    >
      <GlassPanel glow={proposed ? 'amber' : 'none'} className="p-4 hover:border-border-strong">
        <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1.5">
          {/* Category Badge */}
          <span
            className={`inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-[11px] font-medium border ${catConfig.badgeClass}`}
          >
            <CatIcon size={11} />
            {catConfig.label}
          </span>

          <code className="font-mono text-[13px] font-semibold text-fg">{item.key}</code>

          <StatusPill tone={proposed ? 'amber' : item.status === 'CONFIRMED' ? 'teal' : 'neutral'}>
            {item.status.toLowerCase()}
          </StatusPill>

          {item.outcome && item.outcome !== 'active' ? (
            <span
              className={`rounded px-1.5 py-0.5 text-[10px] font-mono uppercase tracking-wider ${
                item.outcome === 'superseded'
                  ? 'bg-zinc-800 text-zinc-400 border border-zinc-700'
                  : item.outcome === 'deprecated'
                    ? 'bg-rose-950/40 text-rose-400 border border-rose-800/40'
                    : 'bg-emerald-950/40 text-emerald-400 border border-emerald-800/40'
              }`}
            >
              {item.outcome}
            </span>
          ) : null}

          {item.supersedes_key ? (
            <span className="inline-flex items-center gap-1 text-[11px] font-mono text-amber/80 bg-amber/10 border border-amber/20 px-1.5 py-0.5 rounded">
              <GitBranch size={10} />
              supersedes: {item.supersedes_key}
            </span>
          ) : null}

          <span className="font-mono text-[11px] text-muted">{item.level}</span>

          {item.scope ? (
            <span
              className={[
                'font-mono text-[11px]',
                item.scope === 'episode_summary' ? 'text-amber font-medium' : 'text-muted',
              ].join(' ')}
            >
              {item.scope}
            </span>
          ) : null}

          {agent ? (
            <span className="inline-flex items-center gap-1 text-[11px] text-muted">
              <Bot size={11} />
              {agent}
            </span>
          ) : null}

          <span className="text-[11px] text-muted">{formatRelative(item.updated_at)}</span>

          <div className="ml-auto flex gap-1">
            {onEdit ? (
              <Button variant="ghost" size="sm" onClick={onEdit} type="button" aria-label="Edit">
                <Pencil size={12} />
                Edit
              </Button>
            ) : null}
            {onHistory ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={onHistory}
                type="button"
                aria-label="Version history"
              >
                <History size={12} />
                History
              </Button>
            ) : null}
            {onShare ? (
              <Button variant="ghost" size="sm" onClick={onShare} type="button" aria-label="Share">
                <Share2 size={12} />
              </Button>
            ) : null}
          </div>
        </div>

        <motion.div
          initial={false}
          animate={{ height: 'auto' }}
          transition={{ duration: 0.22, ease: 'easeOut' }}
        >
          <p className="mt-2.5 whitespace-pre-wrap text-sm leading-relaxed text-fg-dim">
            {preview}
          </p>
        </motion.div>

        {item.content.length > previewLimit ? (
          <button
            type="button"
            className="mt-1 text-[11px] font-medium text-amber hover:underline"
            onClick={() => setExpanded((v) => !v)}
          >
            {expanded ? 'Collapse' : 'Read full text'}
          </button>
        ) : null}

        {/* Concrete Provenance Chips: Files & Tools */}
        {((item.files_affected && item.files_affected.length > 0) ||
          (item.tools_used && item.tools_used.length > 0)) && (
          <div className="mt-3 pt-2.5 border-t border-border/40 flex flex-wrap items-center gap-2">
            {item.files_affected?.map((f) => (
              <span
                key={f}
                className="inline-flex items-center gap-1 font-mono text-[11px] text-fg-dim bg-raised/80 border border-border px-2 py-0.5 rounded-md"
              >
                <FileCode2 size={11} className="text-sky-400" />
                {f}
              </span>
            ))}
            {item.tools_used?.map((t) => (
              <span
                key={t}
                className="inline-flex items-center gap-1 font-mono text-[11px] text-fg-dim bg-raised/80 border border-border px-2 py-0.5 rounded-md"
              >
                <Terminal size={11} className="text-amber" />
                {t}
              </span>
            ))}
          </div>
        )}

        <div className="mt-3 flex flex-wrap items-center gap-2">
          {(item.tags ?? []).map((tag) => (
            <span key={tag} className="font-mono text-[11px] text-muted">
              #{tag}
            </span>
          ))}
          {proposed && onConfirm && onReject ? (
            <div className="ml-auto flex gap-2">
              <Button variant="ghost" size="sm" disabled={busy} onClick={onReject}>
                Reject
              </Button>
              <Button size="sm" disabled={busy} onClick={onConfirm}>
                Confirm
              </Button>
            </div>
          ) : null}
        </div>
      </GlassPanel>
    </motion.div>
  )
}

import { AnimatePresence, motion } from 'framer-motion'
import {
  BookmarkCheck,
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
import { useDeferredValue, useMemo, useState, type ReactNode } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { MemoryEditorSlideOver } from '@/components/MemoryEditorSlideOver'
import { SharingControlsModal } from '@/components/SharingControlsModal'
import { VersionHistoryDrawer } from '@/components/VersionHistoryDrawer'
import { Link, useSearchParams } from 'react-router-dom'
import { Advanced } from '@/components/Advanced'
import { BranchesPage } from './BranchesPage'
import { memoryTitle } from '@/lib/plain-language'
import { looksLikeMarkdown, markdownTitle, stripMarkdown, withoutTitle } from '@/lib/memory-text'
import { agentName } from '@/lib/harness-message'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import { memoryApi, type HarvestJob } from '@/api/memory'
import { useHarvestQueue, useMemorySearch } from '@/hooks/useMemory'
import { isActiveMemory, visibleMemoryStatus } from '@/lib/memory-status'
import { useLocalHarvest } from '@/hooks/useDaemon'
import { usePeople } from '@/hooks/usePeople'
import { useFollowHarvestProject } from '@/hooks/useFollowHarvestProject'
import { useTrustedLocalBridge } from '@/hooks/useTrustedLocalBridge'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError, type MemoryItem } from '@/types/api'
import { formatRelative } from '@/utils/format'
import { useProjectSummaries } from '@/hooks/useProjectSummaries'
import { useProjects } from '@/hooks/useProjects'
import { projectLabel } from '@/lib/project-label'

const CATEGORY_ORDER = [
  'architecture',
  'infrastructure',
  'auth',
  'api',
  'conventions',
  'dependencies',
  'general',
] as const

const MEMORY_LEVELS = [
  { value: '', label: 'All levels' },
  { value: 'organization', label: 'Organization' },
  { value: 'project', label: 'Project' },
  { value: 'personal', label: 'Personal' },
  { value: 'session', label: 'Session' },
  { value: 'ephemeral', label: 'Ephemeral' },
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
    desc: 'Hosting, servers, deploys, CI/CD, containers, ports',
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
    desc: 'API routes, request and response shapes, schemas',
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


function normalizeItemCategory(cat?: string): string {
  if (!cat) return 'general'
  const c = cat.toLowerCase().trim()
  if (c in CATEGORY_CONFIG) return c
  return 'general'
}

/** Monday 00:00 (local) of the week a date falls in. */
function weekStart(d: Date): Date {
  const start = new Date(d.getFullYear(), d.getMonth(), d.getDate())
  start.setDate(start.getDate() - ((start.getDay() + 6) % 7))
  return start
}

/** ISO "2026-W47" → the Monday that week starts on. */
function isoWeekStart(bucket: string): Date | null {
  const m = /^(\d{4})-W(\d{1,2})$/.exec(bucket.trim())
  if (!m) return null
  const jan4 = new Date(Number(m[1]), 0, 4)
  const start = weekStart(jan4)
  start.setDate(start.getDate() + (Number(m[2]) - 1) * 7)
  return start
}

// Each memory's week: the server's ISO week bucket when it sends one, otherwise
// the week it was saved. (Before, every memory without a bucket landed
// in one group labelled with the first memory's date.)
function memoryWeek(item: MemoryItem): Date | null {
  const fromBucket = item.week_bucket ? isoWeekStart(item.week_bucket) : null
  if (fromBucket) return fromBucket
  const when = new Date(item.created_at || item.updated_at || '')
  return isNaN(when.getTime()) ? null : weekStart(when)
}

function formatWeekLabel(start: Date | null): string {
  if (!start) return 'Undated'
  const thisWeek = weekStart(new Date()).getTime()
  const days = Math.round((thisWeek - start.getTime()) / 86400000)
  if (days === 0) return 'This week'
  if (days === 7) return 'Last week'
  const sameYear = start.getFullYear() === new Date().getFullYear()
  return `Week of ${start.toLocaleDateString(undefined, { month: 'short', day: 'numeric', ...(sameYear ? {} : { year: 'numeric' }) })}`
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
  const [level, setLevel] = useState('')
  const [selectedCategory, setSelectedCategory] = useState<string | null>(null)
  const [tagFilter, setTagFilter] = useState<string | null>(null)
  const [moreFilters, setMoreFilters] = useState(false)
  const [params] = useSearchParams()
  // Ctrl K "Search memory for …" links here with ?q=.
  const urlQ = params.get('q')
  const [q, setQ] = useState(() => urlQ ?? '')
  const [seenQ, setSeenQ] = useState(urlQ)
  if (urlQ !== seenQ) {
    setSeenQ(urlQ)
    if (urlQ !== null) setQ(urlQ)
  }
  const deferredQ = useDeferredValue(q)
  const search = useMemorySearch(deferredQ)
  const harvestQ = useHarvestQueue()

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
        title: 'Couldn’t load the full list',
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
      if (level && i.level !== level) return false
      if (tagFilter && !(i.tags ?? []).includes(tagFilter)) return false
      if (selectedCategory && normalizeItemCategory(i.category) !== selectedCategory) return false
      return true
    })
  }, [items, level, tagFilter, selectedCategory])

  const confirmed = useMemo(() => filtered.filter((i) => isActiveMemory(i.status)), [filtered])

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
    const map = new Map<number, { start: Date | null; items: MemoryItem[] }>()
    for (const item of confirmed) {
      const start = memoryWeek(item)
      const key = start ? start.getTime() : -1
      const group = map.get(key) ?? { start, items: [] }
      group.items.push(item)
      map.set(key, group)
    }
    return [...map.entries()]
      .sort(([a], [b]) => b - a)
      .map(([key, g]) => ({ bucket: String(key), label: formatWeekLabel(g.start), items: g.items }))
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

  const harvestTargetId = harvestStatus?.project_id?.trim()
  const harvestMismatch = Boolean(harvestTargetId && projectId && harvestTargetId !== projectId)
  const harvestProject = (projects.data ?? []).find((p) => p.id === harvestTargetId)
  const filtersOn = Boolean(level || tagFilter || selectedCategory)
  const clearFilters = () => {
    setQ('')
    setLevel('')
    setTagFilter(null)
    setSelectedCategory(null)
  }
  // When the open project is empty, point at the project that has the memories.
  const elsewhere = (summaries.data ?? []).find((row) => row.project.id !== projectId && row.metrics.memories > 0)

  return (
    <div className="space-y-8">
      <PageHeader
        title="Memory"
        description="Facts and decisions your agents saved from your chats. The next agent that works on this project can use them."
        actions={
          <>
            <div className="flex rounded-lg border border-border bg-raised/70 p-0.5" role="group" aria-label="View">
              {(
                [
                  ['hierarchy', 'By topic', Layers],
                  ['timeline', 'By week', Calendar],
                ] as const
              ).map(([mode, label, Icon]) => (
                <button
                  key={mode}
                  type="button"
                  aria-pressed={viewMode === mode}
                  onClick={() => setViewMode(mode)}
                  className={[
                    'flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition',
                    viewMode === mode ? 'bg-amber text-ink shadow-sm' : 'text-fg-dim hover:text-fg',
                  ].join(' ')}
                >
                  <Icon size={13} aria-hidden />
                  {label}
                </button>
              ))}
            </div>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void search.refetch()}
              disabled={search.isFetching || !projectId}
              aria-label="Refresh memories"
            >
              <RefreshCw size={13} className={search.isFetching ? 'animate-spin' : ''} />
            </Button>
          </>
        }
      />


      {harvestMismatch && harvestTargetId ? (
        <GlassPanel className="flex flex-wrap items-center justify-between gap-3 border border-amber/40 p-4">
          <p className="text-sm text-fg">
            Desktop is saving new memories to{' '}
            <span className="font-medium">{harvestProject ? projectLabel(harvestProject) : 'another project'}</span>,
            not the one shown here.
          </p>
          <Button type="button" size="sm" onClick={() => setProjectId(harvestTargetId)}>
            Show that project
          </Button>
        </GlassPanel>
      ) : null}

      {harvestInFlight > 0 ? (
        <p className="text-sm text-fg-dim" role="status">
          Reading {harvestInFlight} new {harvestInFlight === 1 ? 'chat' : 'chats'} for things worth remembering… New memories
          appear here on their own.
        </p>
      ) : null}

      {projectId ? (
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-[1fr_auto_auto_auto] sm:items-end">
          <label className="col-span-2 block min-w-0 sm:col-span-1">
            <span className="sr-only">Search memories</span>
            <input
              type="search"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Search memories"
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
            />
          </label>
          <FilterSelect
            label="Topic"
            value={selectedCategory ?? ''}
            onChange={(v) => setSelectedCategory(v || null)}
            options={[['', 'All topics'], ...CATEGORY_ORDER.map((c) => [c, CATEGORY_CONFIG[c].label] as [string, string])]}
          />
          {moreFilters ? (
            <>
              <FilterSelect
                label="Level"
                value={level}
                onChange={setLevel}
                options={MEMORY_LEVELS.map((o) => [o.value, o.label] as [string, string])}
              />
              {allTags.length > 0 ? (
                <FilterSelect
                  label="Tag"
                  value={tagFilter ?? ''}
                  onChange={(v) => setTagFilter(v || null)}
                  options={[['', 'All tags'], ...allTags.map((t) => [t, `#${t}`] as [string, string])]}
                />
              ) : null}
            </>
          ) : (
            <Button type="button" variant="ghost" size="md" onClick={() => setMoreFilters(true)}>
              More filters
            </Button>
          )}
        </div>
      ) : null}

      {filtersOn && (search.hasNext || search.hasPrev) ? (
        <p className="text-[11px] text-muted">Topic, level and tag filters apply to the memories on this page.</p>
      ) : null}

      {search.isLoading ? (
        <p className="text-sm text-muted" role="status">
          Loading memories…
        </p>
      ) : null}

      {search.isError ? (
        <GlassPanel className="flex flex-wrap items-center justify-between gap-3 p-4">
          <p className="text-sm text-danger">
            {search.error instanceof ApiError && search.error.status === 403
              ? 'You’re not a member of this project any more. Pick another project above.'
              : search.error instanceof ApiError
                ? `Couldn’t load memories: ${search.error.message}`
                : 'Couldn’t load memories.'}
          </p>
          <Button type="button" size="sm" variant="secondary" onClick={() => void search.refetch()}>
            Try again
          </Button>
        </GlassPanel>
      ) : null}

      {!projectId && !projects.isLoading ? (
        <GlassPanel className="space-y-2 p-6">
          <p className="text-sm text-fg">No project yet.</p>
          <p className="text-sm text-fg-dim">
            Memories are saved per project. Install the Desktop app and open a code folder, and memories start
            appearing here.
          </p>
          <Link to="/app/connect" className="text-sm text-amber hover:underline">
            Set up the Desktop app →
          </Link>
        </GlassPanel>
      ) : null}

      {projectId && !search.isLoading && !search.isError && confirmed.length === 0 ? (
        <GlassPanel className="space-y-3 p-6">
          {filtersOn || deferredQ ? (
            <>
              <p className="text-sm text-fg-dim">No memories match {deferredQ ? `“${deferredQ}”` : 'these filters'}.</p>
              <Button type="button" size="sm" variant="secondary" onClick={clearFilters}>
                Clear search and filters
              </Button>
            </>
          ) : elsewhere ? (
            <>
              <p className="text-sm text-fg">This project has no memories yet.</p>
              <p className="text-sm text-fg-dim">
                You have {elsewhere.metrics.memories} in <span className="text-fg">{projectLabel(elsewhere.project)}</span>.
              </p>
              <Button type="button" size="sm" onClick={() => setProjectId(elsewhere.project.id)}>
                Show {projectLabel(elsewhere.project)}
              </Button>
            </>
          ) : (
            <p className="text-sm text-fg-dim">
              No memories yet. They appear after you work with an agent in this project while the Desktop app is
              running.
            </p>
          )}
        </GlassPanel>
      ) : null}

      {/* MAIN VIEW: HIERARCHY (ACTIVE TRUTH) */}
      {viewMode === 'hierarchy' && confirmed.length > 0 ? (
        <div className="space-y-6">
          <div className="flex items-center justify-between">
            <h2 className="text-xs font-medium uppercase tracking-wide text-muted">
              {confirmed.length} {confirmed.length === 1 ? 'memory' : 'memories'}
              {search.total > confirmed.length && !filtersOn ? ` of ${search.total}` : ''}
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
              {confirmed.length} {confirmed.length === 1 ? 'memory' : 'memories'} by week
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

      {projectId ? (
        <Advanced
          title="Advanced"
          summary="Try changes on a copy of this project’s memory and merge them back, and see chats still waiting to be read."
          defaultOpen={params.get('advanced') === 'overlays'}
        >
          <div className="space-y-10">
            <div className="space-y-3">
              <h3 className="text-sm font-medium text-fg">Memory copies</h3>
              <p className="text-xs text-fg-dim">
                Make a copy, let an agent change it, compare it with the original, then merge. This is not git.
              </p>
              <BranchesPage embedded />
            </div>
        {/* In-flight harvest batches (raw) */}
        {harvestInFlight > 0 ? (
          <GlassPanel className="space-y-3 p-5">
            <div className="flex items-center justify-between gap-2">
              <h2 className="text-sm font-medium text-fg">Chats waiting to be read</h2>
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
          </div>
        </Advanced>
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

const MEMORY_MD = {
  a: ({ href, children }: { href?: string; children?: ReactNode }) => (
    <a href={href} target="_blank" rel="noreferrer">
      {children}
    </a>
  ),
  // Memories are text; never fetch images from them.
  img: ({ alt }: { alt?: string }) => <span>{alt}</span>,
}

function MemoryCard({
  item,
  onEdit,
  onHistory,
  onShare,
}: {
  item: MemoryItem
  onEdit?: () => void
  onHistory?: () => void
  onShare?: () => void
}) {
  const [expanded, setExpanded] = useState(false)
  const [details, setDetails] = useState(false)
  const name = memoryTitle(item.key)
  const proposed = item.status === 'PROPOSED'
  const { nameFor } = usePeople()
  const by = item.proposed_by ? nameFor(item.proposed_by) : item.source
  // Agent ids ("claude") read as product names; a bare "agent" source reads as "an agent".
  const agent = !by ? '' : by === 'agent' ? 'an agent' : /^[a-z-]+$/.test(by) ? agentName(by) : by
  const previewLimit = item.scope === 'episode_summary' ? 420 : 180
  // Markdown memories (the project brief) take their "# Heading" as the title,
  // preview as plain text and render as markdown when expanded.
  const markdown = looksLikeMarkdown(item.content)
  const heading = markdown ? markdownTitle(item.content) : ''
  const body = heading ? withoutTitle(item.content) : item.content
  const plain = markdown ? stripMarkdown(body) : body
  const long = plain.length > previewLimit
  const preview = long && !expanded ? `${plain.slice(0, previewLimit).trimEnd()}…` : plain

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
        <div className="flex flex-wrap items-start gap-x-3 gap-y-1">
          <div className="min-w-0 flex-1">
            <h3 className="text-sm font-semibold text-fg">
              {heading || name.title}
              {visibleMemoryStatus(item.status) !== 'active' ? (
                <span className="ml-2 align-middle">
                  <StatusPill tone="neutral">{visibleMemoryStatus(item.status)}</StatusPill>
                </span>
              ) : null}
            </h3>
            <p className="mt-0.5 text-xs text-muted">
              {name.group ? `${name.group} · ` : ''}
              {agent ? `Saved by ${agent} · ` : ''}
              {formatRelative(item.updated_at)}
            </p>
          </div>
          <div className="flex gap-1">
            {onEdit ? (
              <Button variant="ghost" size="sm" onClick={onEdit} type="button" aria-label="Edit">
                <Pencil size={12} />
                Edit
              </Button>
            ) : null}
            {onHistory ? (
              <Button variant="ghost" size="sm" onClick={onHistory} type="button" aria-label="Version history">
                <History size={12} />
              </Button>
            ) : null}
            {onShare ? (
              <Button variant="ghost" size="sm" onClick={onShare} type="button" aria-label="Share">
                <Share2 size={12} />
              </Button>
            ) : null}
          </div>
        </div>

        {markdown && expanded ? (
          <div className="memory-prose mt-2">
            <ReactMarkdown remarkPlugins={[remarkGfm]} components={MEMORY_MD}>
              {body}
            </ReactMarkdown>
          </div>
        ) : (
          <p className="mt-2 text-sm leading-relaxed text-fg-dim [overflow-wrap:anywhere] whitespace-pre-wrap">{preview}</p>
        )}

        <div className="mt-1 flex flex-wrap gap-3">
          {long ? (
            <button
              type="button"
              className="text-[11px] font-medium text-amber hover:underline"
              onClick={() => setExpanded((v) => !v)}
            >
              {expanded ? 'Show less' : 'Read more'}
            </button>
          ) : null}
          <button
            type="button"
            className="text-[11px] font-medium text-muted hover:text-fg"
            aria-expanded={details}
            onClick={() => setDetails((v) => !v)}
          >
            {details ? 'Hide details' : 'Details'}
          </button>
        </div>

        {details ? (
          <dl className="mt-3 grid gap-1.5 border-t border-border/40 pt-3 text-[11px] text-fg-dim sm:grid-cols-[8rem_1fr]">
            <dt className="text-muted">Key</dt>
            <dd className="font-mono">{item.key}</dd>
            <dt className="text-muted">Topic</dt>
            <dd className="inline-flex items-center gap-1"><CatIcon size={11} aria-hidden />{catConfig.label}</dd>
            <dt className="text-muted">Visible to</dt>
            <dd>{item.level}{item.scope && item.scope !== item.level ? ` · ${item.scope}` : ''}</dd>
            {item.outcome && item.outcome !== 'active' ? (
              <>
                <dt className="text-muted">Outcome</dt>
                <dd>{item.outcome}</dd>
              </>
            ) : null}
            {item.supersedes_key ? (
              <>
                <dt className="text-muted">Replaces</dt>
                <dd className="inline-flex items-center gap-1 font-mono"><GitBranch size={10} aria-hidden />{item.supersedes_key}</dd>
              </>
            ) : null}
            {item.files_affected?.length ? (
              <>
                <dt className="text-muted">Files</dt>
                <dd className="flex flex-wrap gap-1.5">
                  {item.files_affected.map((f) => (
                    <span key={f} className="inline-flex items-center gap-1 font-mono"><FileCode2 size={11} aria-hidden />{f}</span>
                  ))}
                </dd>
              </>
            ) : null}
            {item.tools_used?.length ? (
              <>
                <dt className="text-muted">Agent tools</dt>
                <dd className="flex flex-wrap gap-1.5">
                  {item.tools_used.map((t) => (
                    <span key={t} className="inline-flex items-center gap-1 font-mono"><Terminal size={11} aria-hidden />{t}</span>
                  ))}
                </dd>
              </>
            ) : null}
            {item.tags?.length ? (
              <>
                <dt className="text-muted">Tags</dt>
                <dd className="font-mono">{item.tags.map((t) => `#${t}`).join(' ')}</dd>
              </>
            ) : null}
          </dl>
        ) : null}
      </GlassPanel>
    </motion.div>
  )
}

function FilterSelect({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  options: [string, string][]
}) {
  return (
    <label className="block">
      <span className="sr-only">{label}</span>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-label={label}
        className="h-10 w-full rounded-lg border border-border bg-raised px-2 text-sm text-fg outline-none focus:border-amber sm:w-auto"
      >
        {options.map(([v, l]) => (
          <option key={v || 'all'} value={v}>
            {l}
          </option>
        ))}
      </select>
    </label>
  )
}

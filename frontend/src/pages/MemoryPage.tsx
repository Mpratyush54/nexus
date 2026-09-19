import { AnimatePresence, motion } from 'framer-motion'
import { Bot, History, Pencil, RefreshCw, Share2 } from 'lucide-react'
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
import {
  useLocalDaemonAutodetect,
  useLocalHarvest,
  useLocalWorkspaceView,
} from '@/hooks/useDaemon'
import { useFollowHarvestProject } from '@/hooks/useFollowHarvestProject'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError, type MemoryItem } from '@/types/api'
import { formatRelative } from '@/utils/format'
import { useProjectSummaries } from '@/hooks/useProjectSummaries'
import { useProjects } from '@/hooks/useProjects'

const LEVEL_FILTERS = ['', 'session', 'project', 'user', 'org'] as const

function projectLabelOf(p: { display_name?: string; folder_name?: string; id: string }) {
  return p.display_name || p.folder_name || `${p.id.slice(0, 8)}…`
}

export function MemoryPage() {
  const { projectId, setProjectId } = useAuth()
  const { push } = useToast()
  const projects = useProjects()
  const summaries = useProjectSummaries()
  const local = useLocalDaemonAutodetect()
  const serverView = useLocalWorkspaceView()
  const bridgeUrl =
    local.data?.baseUrl ||
    local.data?.status.proxy_url ||
    serverView.data?.proxy_url ||
    undefined
  const harvestStatus = useLocalHarvest(bridgeUrl).data
  useFollowHarvestProject(harvestStatus)
  const [q, setQ] = useState('')
  const [level, setLevel] = useState('')
  const [tagFilter, setTagFilter] = useState<string | null>(null)
  const deferredQ = useDeferredValue(q)
  const search = useMemorySearch(deferredQ, level)
  const harvestQ = useHarvestQueue()
  const confirm = useConfirmMemory()
  const reject = useRejectMemory()

  const [expandedHarvest, setExpandedHarvest] = useState<string | null>(null)
  const [harvestFull, setHarvestFull] = useState<Record<string, HarvestJob>>({})
  const [harvestLoading, setHarvestLoading] = useState<string | null>(null)

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
    if (!tagFilter) return items
    return items.filter((i) => (i.tags ?? []).includes(tagFilter))
  }, [items, tagFilter])

  const proposed = filtered.filter((i) => i.status === 'PROPOSED')
  const rest = filtered.filter((i) => i.status !== 'PROPOSED')

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
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-fg">Memory</h1>
          <p className="mt-1 max-w-xl text-sm text-fg-dim">
            Incoming harvest shows raw chat batches while OpenRouter extracts durable facts.
            Finished rows land here as confirmed memories — not PROPOSED scrap.
            {!projectId ? ' Select a project to load memories.' : null}
          </p>
          <div className="mt-3 max-w-xs">
            <ProjectSwitcher compact />
          </div>
        </div>
        <div className="flex gap-2">
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
              Folder <span className="font-mono">central-memory</span> often maps to git remote{' '}
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

      {(harvestQ.data?.length ?? 0) > 0 ? (
        <GlassPanel className="space-y-3 p-5">
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-sm font-medium text-fg">Incoming harvest (raw)</h2>
            <StatusPill tone="teal">{`${harvestQ.data!.length} batches`}</StatusPill>
          </div>
          <ul className="max-h-[28rem] space-y-2 overflow-auto text-sm">
            {harvestQ.data!.slice(0, 20).map((job) => {
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
                      <StatusPill
                        tone={
                          job.status === 'done'
                            ? 'teal'
                            : job.status === 'failed'
                              ? 'danger'
                              : 'amber'
                        }
                      >
                        {job.status}
                      </StatusPill>
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

      <div className="flex flex-wrap items-end gap-3">
        <label className="block min-w-[12rem] flex-1 max-w-md">
          <span className="mb-1.5 block text-xs text-fg-dim">Search</span>
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Filter memories…"
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

      {allTags.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          <button
            type="button"
            onClick={() => setTagFilter(null)}
            className={[
              'rounded-md px-2 py-1 font-mono text-[11px] transition',
              tagFilter === null
                ? 'bg-amber-soft text-amber'
                : 'text-muted hover:bg-raised hover:text-fg',
            ].join(' ')}
          >
            all
          </button>
          {allTags.map((tag) => (
            <button
              key={tag}
              type="button"
              onClick={() => setTagFilter((cur) => (cur === tag ? null : tag))}
              className={[
                'rounded-md px-2 py-1 font-mono text-[11px] transition',
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
            No memories yet. Agent proposals and writes will show up here.
          </p>
        </GlassPanel>
      ) : null}

      {proposed.length > 0 ? (
        <section className="space-y-2.5">
          <h2 className="text-xs font-medium uppercase tracking-wide text-amber">
            Review queue · {proposed.length}
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

      {rest.length > 0 ? (
        <section className="space-y-2.5">
          <h2 className="text-xs font-medium uppercase tracking-wide text-muted">
            Library · {rest.length}
          </h2>
          <AnimatePresence initial={false}>
            {rest.map((item) => (
              <MemoryCard
                key={item.id}
                item={item}
                onEdit={() => setEditing(item)}
                onHistory={() => setHistoryItem(item)}
                onShare={() => setShareItem(item)}
              />
            ))}
          </AnimatePresence>
        </section>
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
  const [expanded, setExpanded] = useState(false)
  const proposed = item.status === 'PROPOSED'
  const agent = item.proposed_by || item.source
  const preview =
    item.content.length > 180 && !expanded
      ? `${item.content.slice(0, 180).trimEnd()}…`
      : item.content

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
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <code className="font-mono text-[13px] text-fg">{item.key}</code>
          <StatusPill tone={proposed ? 'amber' : item.status === 'CONFIRMED' ? 'teal' : 'neutral'}>
            {item.status.toLowerCase()}
          </StatusPill>
          <span className="font-mono text-[11px] text-muted">{item.level}</span>
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

        {item.content.length > 180 ? (
          <button
            type="button"
            className="mt-1 text-[11px] text-muted hover:text-fg"
            onClick={() => setExpanded((v) => !v)}
          >
            {expanded ? 'Collapse' : 'Expand'}
          </button>
        ) : null}

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

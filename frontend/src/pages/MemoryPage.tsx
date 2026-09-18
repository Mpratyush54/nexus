import { useDeferredValue, useState } from 'react'
import { Bot, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import { useConfirmMemory, useMemorySearch, useRejectMemory } from '@/hooks/useMemory'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'

export function MemoryPage() {
  const { projectId } = useAuth()
  const { push } = useToast()
  const [q, setQ] = useState('')
  const deferredQ = useDeferredValue(q)
  const search = useMemorySearch(deferredQ)
  const confirm = useConfirmMemory()
  const reject = useRejectMemory()

  const items = search.data ?? []
  const proposed = items.filter((i) => i.status === 'PROPOSED')
  const rest = items.filter((i) => i.status !== 'PROPOSED')

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

  return (
    <div className="space-y-8">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-fg">Memory</h1>
          <p className="mt-1 text-sm text-fg-dim">
            {projectId
              ? `Project ${projectId.slice(0, 8)}… · live via React Query + WS`
              : 'No project resolved yet — log in again to attach one.'}
          </p>
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

      <label className="block max-w-md">
        <span className="mb-1.5 block text-xs text-fg-dim">Search</span>
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Filter memories…"
          className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
        />
      </label>

      {search.isLoading ? (
        <p className="text-sm text-muted">Loading memories…</p>
      ) : null}

      {search.isError ? (
        <GlassPanel className="p-4">
          <p className="text-sm text-danger">
            {search.error instanceof ApiError
              ? search.error.message
              : 'Failed to load memories'}
          </p>
        </GlassPanel>
      ) : null}

      {!search.isLoading && !search.isError && items.length === 0 ? (
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
          {proposed.map((item) => (
            <MemoryCard
              key={item.id}
              item={item}
              busy={confirm.isPending || reject.isPending}
              onConfirm={() => onConfirm(item.id, item.key)}
              onReject={() => onReject(item.id, item.key)}
            />
          ))}
        </section>
      ) : null}

      {rest.length > 0 ? (
        <section className="space-y-2.5">
          <h2 className="text-xs font-medium uppercase tracking-wide text-muted">
            Library · {rest.length}
          </h2>
          {rest.map((item) => (
            <MemoryCard key={item.id} item={item} />
          ))}
        </section>
      ) : null}
    </div>
  )
}

function MemoryCard({
  item,
  onConfirm,
  onReject,
  busy,
}: {
  item: {
    id: string
    key: string
    content: string
    status: string
    tags?: string[]
    proposed_by?: string
    source?: string
    updated_at: string
  }
  onConfirm?: () => void
  onReject?: () => void
  busy?: boolean
}) {
  const proposed = item.status === 'PROPOSED'
  const agent = item.proposed_by || item.source

  return (
    <GlassPanel glow={proposed ? 'amber' : 'none'} className="p-4 hover:border-border-strong">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <code className="font-mono text-[13px] text-fg">{item.key}</code>
        <StatusPill tone={proposed ? 'amber' : item.status === 'CONFIRMED' ? 'teal' : 'neutral'}>
          {item.status.toLowerCase()}
        </StatusPill>
        {agent ? (
          <span className="inline-flex items-center gap-1 text-[11px] text-muted">
            <Bot size={11} />
            {agent}
          </span>
        ) : null}
        <span className="text-[11px] text-muted">{formatRelative(item.updated_at)}</span>
      </div>
      <p className="mt-2.5 text-sm leading-relaxed text-fg-dim">{item.content}</p>
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
  )
}

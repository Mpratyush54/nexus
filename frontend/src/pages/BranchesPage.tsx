import { motion } from 'framer-motion'
import { GitMerge, Split } from 'lucide-react'
import { useMemo, useState, type FormEvent } from 'react'
import { normalizeDiff } from '@/api/branches'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import {
  useBranchDiff,
  useBranches,
  useCheckoutBranch,
  useCreateBranch,
  useMergeBranches,
} from '@/hooks/useBranches'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'

export function BranchesPage() {
  const { projectId } = useAuth()
  const { push } = useToast()
  const branches = useBranches()
  const create = useCreateBranch()
  const checkout = useCheckoutBranch()
  const merge = useMergeBranches()

  const [name, setName] = useState('')
  const [from, setFrom] = useState('main')
  const [source, setSource] = useState('main')
  const [target, setTarget] = useState('')
  const [showDiff, setShowDiff] = useState(false)

  const diff = useBranchDiff(source, target, showDiff && Boolean(target))
  const normalized = useMemo(
    () => (diff.data ? normalizeDiff(diff.data) : null),
    [diff.data],
  )

  const byId = useMemo(() => {
    const map = new Map<string, string>()
    for (const b of branches.data ?? []) map.set(b.id, b.name)
    return map
  }, [branches.data])

  const tree = useMemo(() => {
    const items = [...(branches.data ?? [])]
    items.sort((a, b) => a.name.localeCompare(b.name))
    return items
  }, [branches.data])

  const onFork = (e: FormEvent) => {
    e.preventDefault()
    const n = name.trim()
    if (!n) return
    create.mutate(
      { name: n, from: from || 'main', visibility: 'shared' },
      {
        onSuccess: () => {
          push({ title: 'Overlay created', detail: n, tone: 'teal' })
          setName('')
          setTarget(n)
        },
        onError: (err) =>
          push({
            title: 'Create failed',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  if (!projectId) {
    return <div className="py-16 text-sm text-muted">Resolve a project first.</div>
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Memory overlays</h1>
        <p className="mt-1 text-sm text-fg-dim">
          Fork project memory into an overlay, diff, and merge back. This is not git or GitHub —
          remotes live under Team.
        </p>
      </div>

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)]">
        <GlassPanel className="space-y-4 p-5">
          <div className="flex items-center gap-2">
            <Split className="h-4 w-4 text-fg-dim" />
            <h2 className="text-sm font-medium text-fg">Overlay tree</h2>
            <StatusPill>{`${tree.length} overlays`}</StatusPill>
          </div>

          <form onSubmit={onFork} className="space-y-2">
            <div className="flex flex-wrap gap-2">
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="overlay-name"
                className="h-9 min-w-[8rem] flex-1 rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none focus:border-amber"
              />
              <select
                value={from}
                onChange={(e) => setFrom(e.target.value)}
                className="h-9 rounded-lg border border-border bg-raised px-2 text-xs text-fg"
              >
                {tree.map((b) => (
                  <option key={b.id} value={b.name}>
                    from {b.name}
                  </option>
                ))}
              </select>
              <Button type="submit" size="sm" disabled={create.isPending}>
                Create overlay
              </Button>
            </div>
          </form>

          <ul className="space-y-1">
            {tree.map((b, i) => {
              const depth = b.parent_branch_id ? 1 : 0
              const parent = b.parent_branch_id ? byId.get(b.parent_branch_id) : null
              return (
                <motion.li
                  key={b.id}
                  initial={{ opacity: 0, x: -6 }}
                  animate={{ opacity: 1, x: 0 }}
                  transition={{ delay: i * 0.03 }}
                  style={{ paddingLeft: depth * 16 }}
                  className="group flex flex-wrap items-center gap-2 rounded-lg border border-transparent px-2 py-2 hover:border-border hover:bg-raised/50"
                >
                  <button
                    type="button"
                    className="min-w-0 flex-1 text-left"
                    onClick={() => {
                      setTarget(b.name)
                      setShowDiff(true)
                    }}
                  >
                    <span className="font-mono text-sm text-fg">{b.name}</span>
                    {parent ? (
                      <span className="ml-2 text-[10px] text-muted">← {parent}</span>
                    ) : null}
                    {b.potentially_stale ? (
                      <StatusPill tone="amber" className="ml-2">
                        stale
                      </StatusPill>
                    ) : null}
                  </button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() =>
                      checkout.mutate(b.name, {
                        onSuccess: () => push({ title: 'Checked out', detail: b.name }),
                        onError: (err) =>
                          push({
                            title: 'Checkout failed',
                            detail: err instanceof ApiError ? err.message : 'Unknown error',
                            tone: 'danger',
                          }),
                      })
                    }
                  >
                    Checkout
                  </Button>
                  <span className="text-[10px] text-muted">{formatRelative(b.created_at)}</span>
                </motion.li>
              )
            })}
            {branches.isLoading ? <li className="text-sm text-muted">Loading…</li> : null}
          </ul>
        </GlassPanel>

        <GlassPanel className="space-y-4 p-5">
          <div className="flex flex-wrap items-center gap-2">
            <GitMerge className="h-4 w-4 text-fg-dim" />
            <h2 className="text-sm font-medium text-fg">Diff & merge</h2>
          </div>

          <div className="flex flex-wrap items-end gap-2">
            <label className="block">
              <span className="mb-1 block text-xs text-fg-dim">Source</span>
              <select
                value={source}
                onChange={(e) => setSource(e.target.value)}
                className="h-9 rounded-lg border border-border bg-raised px-2 text-sm text-fg"
              >
                {tree.map((b) => (
                  <option key={b.id} value={b.name}>
                    {b.name}
                  </option>
                ))}
              </select>
            </label>
            <label className="block">
              <span className="mb-1 block text-xs text-fg-dim">Target</span>
              <select
                value={target}
                onChange={(e) => {
                  setTarget(e.target.value)
                  setShowDiff(true)
                }}
                className="h-9 rounded-lg border border-border bg-raised px-2 text-sm text-fg"
              >
                <option value="">Select…</option>
                {tree.map((b) => (
                  <option key={b.id} value={b.name}>
                    {b.name}
                  </option>
                ))}
              </select>
            </label>
            <Button
              type="button"
              size="sm"
              variant="secondary"
              disabled={!target}
              onClick={() => setShowDiff(true)}
            >
              Compare
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={!target || merge.isPending}
              onClick={() =>
                merge.mutate(
                  { source, target },
                  {
                    onSuccess: () =>
                      push({ title: 'Merged', detail: `${source} → ${target}`, tone: 'teal' }),
                    onError: (err) =>
                      push({
                        title: 'Merge failed',
                        detail: err instanceof ApiError ? err.message : 'Unknown error',
                        tone: 'danger',
                      }),
                  },
                )
              }
            >
              Merge into target
            </Button>
          </div>

          {diff.isFetching ? <p className="text-sm text-muted">Computing diff…</p> : null}
          {diff.isError ? (
            <p className="text-sm text-danger">
              {diff.error instanceof ApiError ? diff.error.message : 'Diff failed'}
            </p>
          ) : null}

          {normalized ? (
            <div className="space-y-4">
              <div className="flex flex-wrap gap-3 text-xs text-fg-dim">
                <span>+{normalized.added.length} added</span>
                <span>−{normalized.removed.length} removed</span>
                <span>~{normalized.modified.length} modified</span>
                <span>={normalized.unchanged.length} unchanged</span>
              </div>

              {normalized.modified.map((c) => (
                <div key={c.key} className="overflow-hidden rounded-lg border border-border">
                  <div className="border-b border-border bg-raised px-3 py-1.5 font-mono text-xs text-amber">
                    {c.key}
                  </div>
                  <div className="grid gap-0 md:grid-cols-2">
                    <pre className="max-h-40 overflow-auto border-b border-border p-3 font-mono text-[11px] leading-relaxed text-danger md:border-b-0 md:border-r">
                      {c.oldContent || '∅'}
                    </pre>
                    <pre className="max-h-40 overflow-auto p-3 font-mono text-[11px] leading-relaxed text-teal">
                      {c.newContent || '∅'}
                    </pre>
                  </div>
                </div>
              ))}

              {normalized.added.map((e) => (
                <div key={`a-${e.key}`} className="rounded-lg border border-teal/25 bg-teal-soft/30 p-3">
                  <p className="font-mono text-xs text-teal">+ {e.key}</p>
                  <pre className="mt-1 max-h-28 overflow-auto font-mono text-[11px] text-fg-dim">
                    {e.content}
                  </pre>
                </div>
              ))}

              {normalized.removed.map((e) => (
                <div key={`r-${e.key}`} className="rounded-lg border border-danger/25 bg-danger-soft/30 p-3">
                  <p className="font-mono text-xs text-danger">− {e.key}</p>
                  <pre className="mt-1 max-h-28 overflow-auto font-mono text-[11px] text-fg-dim">
                    {e.content}
                  </pre>
                </div>
              ))}

              {!normalized.modified.length &&
              !normalized.added.length &&
              !normalized.removed.length ? (
                <p className="text-sm text-muted">No differences between these branches.</p>
              ) : null}
            </div>
          ) : (
            <p className="text-sm text-muted">Select a target branch to preview the diff.</p>
          )}
        </GlassPanel>
      </div>
    </div>
  )
}

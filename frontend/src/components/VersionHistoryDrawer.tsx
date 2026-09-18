import { AnimatePresence, motion } from 'framer-motion'
import { RotateCcw, X } from 'lucide-react'
import { useEffect, useId, useMemo, useState } from 'react'
import { Button } from '@/components/ui/Button'
import { useToast } from '@/components/ui/Toast'
import { useMemoryHistory, useRevertMemory } from '@/hooks/useMemory'
import { ApiError, type MemoryItem, type MemoryVersion } from '@/types/api'
import { formatRelative } from '@/utils/format'

type Props = {
  item: MemoryItem | null
  open: boolean
  onClose: () => void
}

function lineDiff(prev: string, next: string) {
  const a = prev.split('\n')
  const b = next.split('\n')
  const max = Math.max(a.length, b.length)
  const rows: Array<{ type: 'same' | 'add' | 'del'; text: string }> = []
  for (let i = 0; i < max; i++) {
    const left = a[i]
    const right = b[i]
    if (left === right) {
      if (right !== undefined) rows.push({ type: 'same', text: right })
    } else {
      if (left !== undefined) rows.push({ type: 'del', text: left })
      if (right !== undefined) rows.push({ type: 'add', text: right })
    }
  }
  return rows
}

export function VersionHistoryDrawer({ item, open, onClose }: Props) {
  const titleId = useId()
  const { push } = useToast()
  const history = useMemoryHistory(item?.id ?? null, open && Boolean(item))
  const revert = useRevertMemory()
  const [selected, setSelected] = useState<number | null>(null)

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  const versions = useMemo(() => {
    const list = history.data ?? []
    return [...list].sort((a, b) => b.version - a.version)
  }, [history.data])

  useEffect(() => {
    if (versions.length && selected === null) {
      setSelected(versions[0]?.version ?? null)
    }
  }, [versions, selected])

  const active: MemoryVersion | undefined = versions.find((v) => v.version === selected)
  const older = versions.find((v) => active && v.version === active.version - 1)
  const diffRows = active
    ? lineDiff(older?.content ?? '', active.content)
    : []

  const onRevert = (version: number) => {
    if (!item) return
    revert.mutate(
      { id: item.id, version },
      {
        onSuccess: (result) => {
          if (result === null) {
            push({
              title: 'Revert API not ready',
              detail: 'POST /memory/{id}/revert returned 404.',
              tone: 'amber',
            })
            return
          }
          push({ title: 'Reverted', detail: `Restored v${version}`, tone: 'teal' })
          onClose()
        },
        onError: (err) =>
          push({
            title: 'Revert failed',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  const unavailable = history.isSuccess && history.data === null

  return (
    <AnimatePresence>
      {open && item ? (
        <div className="fixed inset-0 z-[72]" role="presentation">
          <motion.button
            type="button"
            aria-label="Close history"
            className="absolute inset-0 border-0 bg-black/55"
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            onClick={onClose}
          />
          <motion.aside
            role="dialog"
            aria-modal="true"
            aria-labelledby={titleId}
            className="absolute inset-y-0 right-0 flex w-full max-w-md flex-col border-l border-border bg-surface shadow-[var(--shadow-glass)]"
            initial={{ x: '100%' }}
            animate={{ x: 0 }}
            exit={{ x: '100%' }}
            transition={{ type: 'spring', stiffness: 380, damping: 34 }}
          >
            <header className="flex items-center justify-between gap-3 border-b border-border px-5 py-4">
              <div className="min-w-0">
                <h2 id={titleId} className="text-sm font-semibold text-fg">
                  Version history
                </h2>
                <p className="mt-0.5 truncate font-mono text-xs text-muted">{item.key}</p>
              </div>
              <button
                type="button"
                className="rounded-lg p-1.5 text-muted hover:bg-raised hover:text-fg"
                onClick={onClose}
                aria-label="Close"
              >
                <X size={16} />
              </button>
            </header>

            <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
              {history.isLoading ? (
                <p className="px-5 py-4 text-sm text-muted">Loading versions…</p>
              ) : null}

              {history.isError ? (
                <p className="px-5 py-4 text-sm text-danger">
                  {history.error instanceof ApiError
                    ? history.error.message
                    : 'Failed to load history'}
                </p>
              ) : null}

              {unavailable ? (
                <div className="space-y-2 px-5 py-4">
                  <p className="text-sm text-fg-dim">
                    Version history API is not available yet (404 on{' '}
                    <code className="font-mono text-xs">GET /memory/{'{id}'}/history</code>).
                  </p>
                  <p className="text-xs text-muted">
                    When the backend ships, this drawer will show a timeline with diffs and
                    one-click revert.
                  </p>
                </div>
              ) : null}

              {!history.isLoading && !unavailable && versions.length === 0 ? (
                <p className="px-5 py-4 text-sm text-fg-dim">No prior versions yet.</p>
              ) : null}

              {versions.length > 0 ? (
                <div className="grid min-h-0 flex-1 grid-rows-[auto_1fr]">
                  <ul className="max-h-44 space-y-1 overflow-y-auto border-b border-border px-3 py-3">
                    {versions.map((v) => (
                      <li key={v.version}>
                        <button
                          type="button"
                          onClick={() => setSelected(v.version)}
                          className={[
                            'flex w-full items-center justify-between gap-2 rounded-lg px-3 py-2 text-left transition',
                            selected === v.version
                              ? 'bg-raised text-fg'
                              : 'text-fg-dim hover:bg-raised/60 hover:text-fg',
                          ].join(' ')}
                        >
                          <span className="font-mono text-xs">v{v.version}</span>
                          <span className="text-[11px] text-muted">
                            {formatRelative(v.created_at)}
                          </span>
                        </button>
                      </li>
                    ))}
                  </ul>

                  <div className="min-h-0 overflow-y-auto px-5 py-4">
                    {active ? (
                      <>
                        <div className="mb-3 flex items-center justify-between gap-2">
                          <p className="text-xs text-fg-dim">
                            Diff vs {older ? `v${older.version}` : 'empty'}
                          </p>
                          <Button
                            size="sm"
                            variant="secondary"
                            disabled={revert.isPending}
                            onClick={() => onRevert(active.version)}
                          >
                            <RotateCcw size={12} />
                            Revert
                          </Button>
                        </div>
                        <pre className="overflow-x-auto rounded-lg border border-border bg-base p-3 font-mono text-[11px] leading-5">
                          {diffRows.map((row, i) => (
                            <div
                              key={`${row.type}-${i}`}
                              className={
                                row.type === 'add'
                                  ? 'bg-teal-soft text-teal'
                                  : row.type === 'del'
                                    ? 'bg-danger-soft text-danger'
                                    : 'text-fg-dim'
                              }
                            >
                              <span className="mr-2 select-none opacity-60">
                                {row.type === 'add' ? '+' : row.type === 'del' ? '−' : ' '}
                              </span>
                              {row.text || ' '}
                            </div>
                          ))}
                        </pre>
                      </>
                    ) : null}
                  </div>
                </div>
              ) : null}
            </div>
          </motion.aside>
        </div>
      ) : null}
    </AnimatePresence>
  )
}

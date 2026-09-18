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

function currentFromItem(item: MemoryItem): MemoryVersion {
  return {
    memory_id: item.id,
    version: 0,
    key: item.key,
    content: item.content,
    tags: item.tags,
    level: item.level,
    scope: item.scope,
    edited_by: item.proposed_by,
    created_at: item.updated_at,
  }
}

function versionLabel(v: MemoryVersion) {
  return v.version === 0 ? 'Current' : `v${v.version}`
}

export function VersionHistoryDrawer({ item, open, onClose }: Props) {
  const titleId = useId()
  const { push } = useToast()
  const history = useMemoryHistory(item?.id ?? null, open && Boolean(item))
  const revert = useRevertMemory()
  const [selected, setSelected] = useState(0)

  useEffect(() => {
    if (!open) return
    setSelected(0)
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose, item?.id])

  const versions = useMemo(() => {
    if (!item) return []
    const snapshots = [...(history.data?.items ?? [])].sort((a, b) => b.version - a.version)
    const current = history.data?.current ?? currentFromItem(item)
    return [current, ...snapshots]
  }, [history.data, item])

  const active: MemoryVersion | undefined = versions.find((v) => v.version === selected) ?? versions[0]
  const olderIndex = versions.findIndex((v) => v.version === active?.version)
  const older = olderIndex >= 0 ? versions[olderIndex + 1] : undefined
  const diffRows = active ? lineDiff(older?.content ?? '', active.content) : []

  const onRevert = (version: number) => {
    if (!item || version < 1) return
    revert.mutate(
      { id: item.id, version },
      {
        onSuccess: (result) => {
          if (result === null) {
            push({
              title: 'Revert failed',
              detail: 'The history API is not available.',
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

              {!history.isLoading && versions.length > 0 ? (
                <div className="grid min-h-0 flex-1 grid-rows-[auto_1fr]">
                  <ul className="max-h-52 space-y-1 overflow-y-auto border-b border-border px-3 py-3">
                    {versions.map((v) => (
                      <li key={`${v.memory_id ?? item.id}-${v.version}`}>
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
                          <span className="flex min-w-0 items-center gap-2">
                            <span className="font-mono text-xs">{versionLabel(v)}</span>
                            {v.edited_by ? (
                              <span className="truncate text-[11px] text-muted">{v.edited_by}</span>
                            ) : null}
                          </span>
                          <span className="shrink-0 text-[11px] text-muted">
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
                            {active.version === 0
                              ? older
                                ? `Live vs ${versionLabel(older)}`
                                : 'Current content'
                              : `Diff vs ${older ? versionLabel(older) : 'empty'}`}
                          </p>
                          {active.version >= 1 ? (
                            <Button
                              size="sm"
                              variant="secondary"
                              disabled={revert.isPending}
                              onClick={() => onRevert(active.version)}
                            >
                              <RotateCcw size={12} />
                              Restore
                            </Button>
                          ) : (
                            <span className="text-[11px] text-muted">Live version</span>
                          )}
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

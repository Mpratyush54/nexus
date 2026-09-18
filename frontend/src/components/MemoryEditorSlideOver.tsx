import { AnimatePresence, motion } from 'framer-motion'
import { History, Share2, X } from 'lucide-react'
import { useEffect, useId, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/Button'
import { useToast } from '@/components/ui/Toast'
import { useUpdateMemory } from '@/hooks/useMemory'
import { ApiError, type MemoryItem } from '@/types/api'

const LEVELS = ['session', 'project', 'user', 'org'] as const
const SCOPES = ['private', 'shared', 'project', 'public'] as const

type Props = {
  item: MemoryItem | null
  open: boolean
  onClose: () => void
  onOpenHistory?: () => void
  onOpenShare?: () => void
}

export function MemoryEditorSlideOver({
  item,
  open,
  onClose,
  onOpenHistory,
  onOpenShare,
}: Props) {
  const titleId = useId()
  const { push } = useToast()
  const update = useUpdateMemory()

  const [key, setKey] = useState('')
  const [content, setContent] = useState('')
  const [tagsRaw, setTagsRaw] = useState('')
  const [level, setLevel] = useState('project')
  const [scope, setScope] = useState('project')

  useEffect(() => {
    if (!item) return
    setKey(item.key)
    setContent(item.content)
    setTagsRaw((item.tags ?? []).join(', '))
    setLevel(item.level || 'project')
    setScope(item.scope || 'project')
  }, [item])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  const locked = item
    ? item.status !== 'PROPOSED' && item.status !== 'CONFIRMED'
    : true

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!item || locked) return

    const tags = tagsRaw
      .split(/[,#\s]+/)
      .map((t) => t.trim())
      .filter(Boolean)

    update.mutate(
      {
        id: item.id,
        payload: {
          key: key.trim(),
          content,
          tags,
          level,
          scope,
        },
      },
      {
        onSuccess: (result) => {
          if (result === null) {
            push({
              title: 'Edit API not ready',
              detail: 'PUT /memory/{id} returned 404 — UI is wired for when it ships.',
              tone: 'amber',
            })
            return
          }
          push({ title: 'Memory updated', detail: result.key, tone: 'teal' })
          onClose()
        },
        onError: (err) =>
          push({
            title: 'Save failed',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  return (
    <AnimatePresence>
      {open && item ? (
        <div className="fixed inset-0 z-[70]" role="presentation">
          <motion.button
            type="button"
            aria-label="Close editor"
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
            className="absolute inset-y-0 right-0 flex w-full max-w-lg flex-col border-l border-border bg-surface shadow-[var(--shadow-glass)]"
            initial={{ x: '100%' }}
            animate={{ x: 0 }}
            exit={{ x: '100%' }}
            transition={{ type: 'spring', stiffness: 380, damping: 34 }}
          >
            <header className="flex items-center justify-between gap-3 border-b border-border px-5 py-4">
              <div>
                <h2 id={titleId} className="text-sm font-semibold text-fg">
                  Edit memory
                </h2>
                <p className="mt-0.5 text-xs text-muted">
                  Key, content, tags, level & scope
                </p>
              </div>
              <div className="flex items-center gap-1">
                {onOpenHistory ? (
                  <Button variant="ghost" size="sm" onClick={onOpenHistory} type="button">
                    <History size={13} />
                    History
                  </Button>
                ) : null}
                {onOpenShare ? (
                  <Button variant="ghost" size="sm" onClick={onOpenShare} type="button">
                    <Share2 size={13} />
                    Share
                  </Button>
                ) : null}
                <button
                  type="button"
                  className="rounded-lg p-1.5 text-muted hover:bg-raised hover:text-fg"
                  onClick={onClose}
                  aria-label="Close"
                >
                  <X size={16} />
                </button>
              </div>
            </header>

            <form onSubmit={onSubmit} className="flex min-h-0 flex-1 flex-col">
              <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">
                {locked ? (
                  <p className="rounded-lg border border-border bg-raised px-3 py-2 text-xs text-fg-dim">
                    {item.status} memories are locked — only PROPOSED / CONFIRMED can be edited.
                  </p>
                ) : null}

                <label className="block">
                  <span className="mb-1.5 block text-xs text-fg-dim">Key</span>
                  <input
                    value={key}
                    onChange={(e) => setKey(e.target.value)}
                    disabled={locked}
                    required
                    className="h-10 w-full rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none transition focus:border-amber disabled:opacity-50"
                  />
                </label>

                <label className="block">
                  <div className="mb-1.5 flex items-center justify-between gap-2">
                    <span className="text-xs text-fg-dim">Content</span>
                    <span className="font-mono text-[11px] text-muted">
                      {content.length.toLocaleString()} chars
                    </span>
                  </div>
                  <textarea
                    value={content}
                    onChange={(e) => setContent(e.target.value)}
                    disabled={locked}
                    required
                    rows={12}
                    className="w-full resize-y rounded-lg border border-border bg-raised px-3 py-2.5 font-mono text-[13px] leading-relaxed text-fg outline-none transition focus:border-amber disabled:opacity-50"
                  />
                </label>

                <label className="block">
                  <span className="mb-1.5 block text-xs text-fg-dim">
                    Tags <span className="text-muted">(comma-separated)</span>
                  </span>
                  <input
                    value={tagsRaw}
                    onChange={(e) => setTagsRaw(e.target.value)}
                    disabled={locked}
                    placeholder="auth, api, decision"
                    className="h-10 w-full rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none transition focus:border-amber disabled:opacity-50"
                  />
                </label>

                <div className="grid grid-cols-2 gap-3">
                  <label className="block">
                    <span className="mb-1.5 block text-xs text-fg-dim">Level</span>
                    <select
                      value={level}
                      onChange={(e) => setLevel(e.target.value)}
                      disabled={locked}
                      className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber disabled:opacity-50"
                    >
                      {LEVELS.map((l) => (
                        <option key={l} value={l}>
                          {l}
                        </option>
                      ))}
                      {!LEVELS.includes(level as (typeof LEVELS)[number]) ? (
                        <option value={level}>{level}</option>
                      ) : null}
                    </select>
                  </label>
                  <label className="block">
                    <span className="mb-1.5 block text-xs text-fg-dim">Scope</span>
                    <select
                      value={scope}
                      onChange={(e) => setScope(e.target.value)}
                      disabled={locked}
                      className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber disabled:opacity-50"
                    >
                      {SCOPES.map((s) => (
                        <option key={s} value={s}>
                          {s}
                        </option>
                      ))}
                      {!SCOPES.includes(scope as (typeof SCOPES)[number]) ? (
                        <option value={scope}>{scope}</option>
                      ) : null}
                    </select>
                  </label>
                </div>
              </div>

              <footer className="flex items-center justify-end gap-2 border-t border-border px-5 py-4">
                <Button type="button" variant="ghost" size="sm" onClick={onClose}>
                  Cancel
                </Button>
                <Button type="submit" size="sm" disabled={locked || update.isPending}>
                  {update.isPending ? 'Saving…' : 'Save changes'}
                </Button>
              </footer>
            </form>
          </motion.aside>
        </div>
      ) : null}
    </AnimatePresence>
  )
}

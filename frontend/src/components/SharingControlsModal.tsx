import { AnimatePresence, motion } from 'framer-motion'
import { Copy, X } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { Button } from '@/components/ui/Button'
import { useToast } from '@/components/ui/Toast'
import type { MemoryItem, MemoryShareVisibility } from '@/types/api'

const VISIBILITY: Array<{ id: MemoryShareVisibility; label: string; blurb: string }> = [
  { id: 'private', label: 'Private', blurb: 'Only you' },
  { id: 'shared', label: 'Shared', blurb: 'Selected teammates' },
  { id: 'project', label: 'Project', blurb: 'Everyone on this project' },
  { id: 'public', label: 'Public', blurb: 'Org-wide read' },
]

type Props = {
  item: MemoryItem | null
  open: boolean
  onClose: () => void
}

/**
 * Sharing modal stub — UI ready for POST/GET /memory/{id}/share when the API lands.
 * Actions toast a "coming soon" notice rather than calling missing endpoints.
 */
export function SharingControlsModal({ item, open, onClose }: Props) {
  const titleId = useId()
  const { push } = useToast()
  const [visibility, setVisibility] = useState<MemoryShareVisibility>('project')

  useEffect(() => {
    if (!item) return
    const scope = (item.scope || 'project').toLowerCase()
    if (scope === 'private' || scope === 'shared' || scope === 'project' || scope === 'public') {
      setVisibility(scope)
    }
  }, [item])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  const stub = (action: string) => {
    push({
      title: 'Sharing API not ready',
      detail: `${action} — POST /memory/{id}/share will wire here.`,
      tone: 'amber',
    })
  }

  return (
    <AnimatePresence>
      {open && item ? (
        <div className="fixed inset-0 z-[74] flex items-center justify-center p-4" role="presentation">
          <motion.button
            type="button"
            aria-label="Close sharing"
            className="absolute inset-0 border-0 bg-black/55"
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            onClick={onClose}
          />
          <motion.div
            role="dialog"
            aria-modal="true"
            aria-labelledby={titleId}
            className="relative w-full max-w-md rounded-xl border border-border bg-surface p-5 shadow-[var(--shadow-glass)]"
            initial={{ opacity: 0, y: 12, scale: 0.97 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: 8, scale: 0.97 }}
            transition={{ type: 'spring', stiffness: 400, damping: 30 }}
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <h2 id={titleId} className="text-sm font-semibold text-fg">
                  Share memory
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
            </div>

            <fieldset className="mt-4 space-y-2">
              <legend className="mb-2 text-xs text-fg-dim">Visibility</legend>
              {VISIBILITY.map((opt) => (
                <label
                  key={opt.id}
                  className={[
                    'flex cursor-pointer items-center gap-3 rounded-lg border px-3 py-2.5 transition',
                    visibility === opt.id
                      ? 'border-amber/40 bg-amber-soft'
                      : 'border-border hover:border-border-strong',
                  ].join(' ')}
                >
                  <input
                    type="radio"
                    name="visibility"
                    value={opt.id}
                    checked={visibility === opt.id}
                    onChange={() => setVisibility(opt.id)}
                    className="accent-amber"
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block text-sm text-fg">{opt.label}</span>
                    <span className="block text-[11px] text-muted">{opt.blurb}</span>
                  </span>
                </label>
              ))}
            </fieldset>

            <div className="mt-5 flex flex-wrap items-center justify-between gap-2">
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={() => stub('Cross-project copy')}
              >
                <Copy size={13} />
                Copy to project…
              </Button>
              <div className="flex gap-2">
                <Button type="button" variant="ghost" size="sm" onClick={onClose}>
                  Cancel
                </Button>
                <Button type="button" size="sm" onClick={() => stub(`Set visibility → ${visibility}`)}>
                  Apply
                </Button>
              </div>
            </div>
          </motion.div>
        </div>
      ) : null}
    </AnimatePresence>
  )
}

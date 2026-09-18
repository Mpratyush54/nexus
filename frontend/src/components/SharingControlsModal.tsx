import { AnimatePresence, motion } from 'framer-motion'
import { Copy, Trash2, X } from 'lucide-react'
import { useEffect, useId, useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { memoryApi, type MemoryShare } from '@/api/memory'
import { Button } from '@/components/ui/Button'
import { useToast } from '@/components/ui/Toast'
import { queryKeys } from '@/lib/query-keys'
import type { MemoryItem, MemoryShareVisibility } from '@/types/api'
import { ApiError } from '@/types/api'

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

export function SharingControlsModal({ item, open, onClose }: Props) {
  const titleId = useId()
  const { push } = useToast()
  const qc = useQueryClient()
  const [visibility, setVisibility] = useState<MemoryShareVisibility>('project')
  const [userId, setUserId] = useState('')
  const [role, setRole] = useState('')
  const [copyProject, setCopyProject] = useState('')
  const [busy, setBusy] = useState(false)

  const shares = useQuery({
    queryKey: queryKeys.memory.shares(item?.id ?? ''),
    enabled: open && Boolean(item?.id),
    queryFn: async () => (await memoryApi.shares(item!.id)).items,
  })

  useEffect(() => {
    if (!item) return
    const vis = (item.visibility || 'project').toLowerCase()
    if (vis === 'private' || vis === 'shared' || vis === 'project' || vis === 'public') {
      setVisibility(vis)
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

  const fail = (title: string, err: unknown) => {
    push({
      title,
      detail: err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Unknown error',
      tone: 'danger',
    })
  }

  const refresh = (id: string) => {
    void qc.invalidateQueries({ queryKey: queryKeys.memory.shares(id) })
    void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
  }

  const onApply = async () => {
    if (!item) return
    setBusy(true)
    try {
      await memoryApi.share(item.id, { visibility })
      push({ title: 'Visibility updated', detail: visibility, tone: 'teal' })
      refresh(item.id)
    } catch (err) {
      fail('Could not set visibility', err)
    } finally {
      setBusy(false)
    }
  }

  const onGrant = async (e: FormEvent) => {
    e.preventDefault()
    if (!item) return
    const uid = userId.trim()
    const r = role.trim()
    if ((uid && r) || (!uid && !r)) {
      push({ title: 'Provide a user id or a role, not both', tone: 'amber' })
      return
    }
    setBusy(true)
    try {
      await memoryApi.share(item.id, uid ? { user_id: uid } : { role: r })
      push({ title: 'Share granted', detail: uid || r, tone: 'teal' })
      setUserId('')
      setRole('')
      refresh(item.id)
    } catch (err) {
      fail('Could not share', err)
    } finally {
      setBusy(false)
    }
  }

  const onUnshare = async (share: MemoryShare) => {
    if (!item || !share.shared_with_user_id) return
    setBusy(true)
    try {
      await memoryApi.unshare(item.id, share.shared_with_user_id)
      push({ title: 'Share revoked' })
      refresh(item.id)
    } catch (err) {
      fail('Could not unshare', err)
    } finally {
      setBusy(false)
    }
  }

  const onCopy = async () => {
    if (!item) return
    const dest = copyProject.trim()
    if (!dest) return
    setBusy(true)
    try {
      const dup = await memoryApi.copy(item.id, dest)
      push({ title: 'Copied to project', detail: dup.key, tone: 'teal' })
      setCopyProject('')
    } catch (err) {
      fail('Copy failed', err)
    } finally {
      setBusy(false)
    }
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

            <form onSubmit={onGrant} className="mt-4 grid gap-2">
              <p className="text-xs text-fg-dim">Grant to a user or role</p>
              <div className="flex gap-2">
                <input
                  value={userId}
                  onChange={(e) => setUserId(e.target.value)}
                  placeholder="user id"
                  className="h-9 min-w-0 flex-1 rounded-lg border border-border bg-raised px-3 font-mono text-xs text-fg outline-none focus:border-amber"
                />
                <input
                  value={role}
                  onChange={(e) => setRole(e.target.value)}
                  placeholder="role"
                  className="h-9 w-24 rounded-lg border border-border bg-raised px-3 text-xs text-fg outline-none focus:border-amber"
                />
                <Button type="submit" size="sm" disabled={busy}>
                  Grant
                </Button>
              </div>
            </form>

            <ul className="mt-3 max-h-32 space-y-1 overflow-y-auto">
              {(shares.data ?? []).map((sh) => (
                <li
                  key={sh.id}
                  className="flex items-center justify-between gap-2 rounded-md border border-border px-2 py-1.5 text-xs"
                >
                  <span className="truncate font-mono text-fg">
                    {sh.shared_with_user_id || sh.shared_with_role || 'grant'}
                  </span>
                  {sh.shared_with_user_id ? (
                    <button
                      type="button"
                      className="rounded p-1 text-muted hover:text-danger"
                      onClick={() => void onUnshare(sh)}
                      aria-label="Revoke"
                    >
                      <Trash2 size={12} />
                    </button>
                  ) : null}
                </li>
              ))}
              {shares.isLoading ? <li className="text-xs text-muted">Loading shares…</li> : null}
            </ul>

            <div className="mt-4 flex gap-2">
              <input
                value={copyProject}
                onChange={(e) => setCopyProject(e.target.value)}
                placeholder="Copy to project id"
                className="h-9 min-w-0 flex-1 rounded-lg border border-border bg-raised px-3 font-mono text-xs text-fg outline-none focus:border-amber"
              />
              <Button type="button" variant="secondary" size="sm" disabled={busy || !copyProject.trim()} onClick={() => void onCopy()}>
                <Copy size={13} />
                Copy
              </Button>
            </div>

            <div className="mt-5 flex justify-end gap-2">
              <Button type="button" variant="ghost" size="sm" onClick={onClose}>
                Cancel
              </Button>
              <Button type="button" size="sm" disabled={busy} onClick={() => void onApply()}>
                Apply visibility
              </Button>
            </div>
          </motion.div>
        </div>
      ) : null}
    </AnimatePresence>
  )
}

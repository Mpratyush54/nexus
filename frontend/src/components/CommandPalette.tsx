import { AnimatePresence, motion } from 'framer-motion'
import { Activity, Bell, Bot, Building2, Cable, LayoutDashboard, Search, Settings, Shield, Split, Users } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'

const items = [
  { id: 'dashboard', label: 'Home', hint: 'Project memories, scanner, pulse', icon: LayoutDashboard, to: '/app/dashboard' },
  { id: 'memory', label: 'Memory', hint: 'Review queue & editor', icon: Search, to: '/app/memory' },
  { id: 'agents', label: 'Agents', hint: 'MCP activity & permissions', icon: Bot, to: '/app/agents' },
  { id: 'team', label: 'Team', hint: 'Members, roles, GitHub', icon: Users, to: '/app/team' },
  { id: 'org', label: 'Org', hint: 'Organizations, projects, billing', icon: Building2, to: '/app/org' },
  { id: 'branches', label: 'Overlays', hint: 'Memory fork / diff / merge (not git)', icon: Split, to: '/app/branches' },
  { id: 'activity', label: 'Activity', hint: 'Project event log with filters', icon: Activity, to: '/app/activity' },
  { id: 'notifications', label: 'Notifications', hint: 'Inbox, preferences, devices', icon: Bell, to: '/app/notifications' },
  { id: 'connect', label: 'Desktop setup', hint: 'Install Desktop + harvest', icon: Cable, to: '/app/connect' },
  { id: 'onboarding', label: 'Onboarding', hint: 'First-run setup guide', icon: Cable, to: '/app/onboarding' },
  { id: 'settings', label: 'Settings', hint: 'Tokens, plan, export', icon: Settings, to: '/app/settings' },
  { id: 'admin', label: 'Platform', hint: 'Super Admin, versions, CLI releases', icon: Shield, to: '/app/admin' },
]

type Props = {
  open: boolean
  onClose: () => void
}

export function CommandPalette({ open, onClose }: Props) {
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return items
    return items.filter(
      (i) => i.label.toLowerCase().includes(q) || i.hint.toLowerCase().includes(q),
    )
  }, [query])

  useEffect(() => {
    if (!open) {
      setQuery('')
      setActive(0)
    }
  }, [open])

  useEffect(() => {
    setActive(0)
  }, [query])

  useEffect(() => {
    if (!open) return

    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setActive((i) => Math.min(i + 1, Math.max(filtered.length - 1, 0)))
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setActive((i) => Math.max(i - 1, 0))
      }
      if (e.key === 'Enter' && filtered[active]) {
        e.preventDefault()
        navigate(filtered[active].to)
        onClose()
      }
    }

    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, filtered, active, navigate, onClose])

  return (
    <AnimatePresence>
      {open ? (
        <motion.div
          className="fixed inset-0 z-[70] flex items-start justify-center px-4 pt-[18vh]"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
        >
          <button
            type="button"
            className="absolute inset-0 bg-black/65 backdrop-blur-sm"
            aria-label="Close command palette"
            onClick={onClose}
          />
          <motion.div
            role="dialog"
            aria-modal="true"
            aria-label="Command palette"
            initial={{ opacity: 0, y: -10, scale: 0.98 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: -6, scale: 0.98 }}
            transition={{ type: 'spring', stiffness: 420, damping: 30 }}
            className="relative w-full max-w-lg overflow-hidden rounded-xl border border-border bg-surface shadow-[var(--shadow-glass)]"
          >
            <div className="flex items-center gap-3 border-b border-border px-4 py-3">
              <Search size={16} className="text-muted" />
              <input
                autoFocus
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Jump to memory, agents, team…"
                className="w-full bg-transparent text-sm text-fg outline-none placeholder:text-muted"
              />
              <kbd className="font-mono hidden rounded border border-border px-1.5 py-0.5 text-[10px] text-muted sm:inline">
                esc
              </kbd>
            </div>
            <ul className="max-h-72 overflow-auto p-2">
              {filtered.length === 0 ? (
                <li className="px-3 py-6 text-center text-sm text-muted">No matches</li>
              ) : (
                filtered.map((item, i) => {
                  const Icon = item.icon
                  return (
                    <li key={item.id}>
                      <button
                        type="button"
                        onMouseEnter={() => setActive(i)}
                        onClick={() => {
                          navigate(item.to)
                          onClose()
                        }}
                        className={[
                          'flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left transition',
                          i === active ? 'bg-raised' : 'hover:bg-raised/60',
                        ].join(' ')}
                      >
                        <span className="flex h-8 w-8 items-center justify-center rounded-md border border-border bg-base text-fg-dim">
                          <Icon size={14} />
                        </span>
                        <span className="min-w-0 flex-1">
                          <span className="block text-sm font-medium text-fg">{item.label}</span>
                          <span className="block truncate text-xs text-muted">{item.hint}</span>
                        </span>
                      </button>
                    </li>
                  )
                })
              )}
            </ul>
          </motion.div>
        </motion.div>
      ) : null}
    </AnimatePresence>
  )
}

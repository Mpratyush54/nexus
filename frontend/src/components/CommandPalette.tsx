import { AnimatePresence, motion, useReducedMotion } from 'framer-motion'
import {
  Activity, Bell, Bot, Brain, Building2, Cable, History, Keyboard, LayoutDashboard, MessageSquare, Moon,
  Plus, Search, Send, Settings, Shield, Smartphone, Split, Square, Sun, Users, type LucideIcon,
} from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient, type InfiniteData } from '@tanstack/react-query'
import { STEER_ROUTE } from '@/lib/navigation'
import { chatsApi, type ChatCard, type ChatPage } from '@/api/chats'
import { memoryApi } from '@/api/memory'
import { useAuth } from '@/providers/AuthProvider'
import { agentName, chatTitle } from '@/lib/harness-message'
import { useChatPrefs } from '@/lib/chat-prefs'
import { DESKTOP_RELAY } from '@/lib/env'
import { memoryTitle } from '@/lib/plain-language'
import { resolveTheme, getThemePreference, setThemePreference } from '@/lib/theme'
import { comboLabel, runAppCommand, type AppCommand } from '@/lib/shortcuts'
import { formatRelative } from '@/utils/format'

type Group = 'Actions' | 'Chats' | 'Memories' | 'Go to' | 'Search'
type Item = { id: string; label: string; hint: string; icon: LucideIcon; group: Group; run: () => void; keys?: string[] }

const pages = [
  { id: 'dashboard', label: 'Home', hint: 'Recent chats and memories', icon: LayoutDashboard, to: '/app/dashboard' },
  { id: 'chats', label: 'Chats', hint: 'Read and search your agent chats', icon: History, to: '/app/chats' },
  { id: 'memory', label: 'Memory', hint: 'What your agents learned', icon: Search, to: '/app/memory' },
  { id: 'activity', label: 'Activity', hint: 'What your agents did, day by day', icon: Activity, to: '/app/activity' },
  { id: 'steer', label: 'Phone & devices', hint: 'Message an agent on your computer', icon: Smartphone, to: STEER_ROUTE },
  { id: 'team', label: 'Team', hint: 'Who can see this project', icon: Users, to: '/app/team' },
  { id: 'org', label: 'Organization', hint: 'Members, roles, invites, projects', icon: Building2, to: '/app/org' },
  { id: 'connect', label: 'Desktop app', hint: 'Install and connect the desktop app', icon: Cable, to: '/app/connect' },
  { id: 'teleport', label: 'Chats sent to you', hint: 'Phone & devices', icon: Send, to: `${STEER_ROUTE}#sent-to-you` },
  { id: 'agents', label: 'Agent connections', hint: 'Activity → Advanced', icon: Bot, to: '/app/activity?advanced=agents' },
  { id: 'branches', label: 'Memory copies', hint: 'Memory → Advanced', icon: Split, to: '/app/memory?advanced=overlays' },
  { id: 'notifications', label: 'Notifications', hint: 'Inbox, preferences, devices', icon: Bell, to: '/app/notifications' },
  { id: 'onboarding', label: 'Setup guide', hint: 'First-run setup', icon: Cable, to: '/app/onboarding' },
  { id: 'settings', label: 'Settings', hint: 'Account, plan, tokens', icon: Settings, to: '/app/settings' },
  { id: 'admin', label: 'Platform', hint: 'Super Admin, versions, CLI releases', icon: Shield, to: '/app/admin' },
]

const GROUP_ORDER: Group[] = ['Actions', 'Chats', 'Memories', 'Search', 'Go to']

function matches(q: string, ...fields: (string | undefined)[]) {
  if (!q) return true
  const hay = fields.join(' ').toLowerCase()
  return q.split(/\s+/).every((w) => hay.includes(w))
}

/** Waits until typing pauses, so each key press doesn't start a search. */
function useDebounced(value: string, ms: number) {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

type Props = {
  open: boolean
  onClose: () => void
  onShortcuts?: () => void
}

export function CommandPalette({ open, onClose, onShortcuts }: Props) {
  const navigate = useNavigate()
  const location = useLocation()
  const cache = useQueryClient()
  const { projectId, user } = useAuth()
  const prefs = useChatPrefs(user?.userId ?? '')
  const reduced = useReducedMotion()
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const list = useRef<HTMLUListElement>(null)
  const raw = query.trim()
  const q = raw.toLowerCase()
  const searchQ = useDebounced(raw, 150)
  const onChat = /\/app\/chats/.test(location.pathname) && new URLSearchParams(location.search).has('chat')

  // Chats already loaded for the list show instantly; the server fills in matches by content.
  const cachedChats = useMemo(() => {
    if (!open || !projectId) return [] as ChatCard[]
    const data = cache.getQueryData<InfiniteData<ChatPage<ChatCard>>>(['chats', projectId, ''])
    return data?.pages.flatMap((p) => p.items) ?? []
  }, [open, projectId, cache])
  const serverChats = useQuery({
    queryKey: ['palette-chats', projectId, searchQ],
    enabled: open && !!projectId && searchQ.length >= 2,
    queryFn: ({ signal }) => chatsApi.list(projectId!, searchQ, '', signal),
    staleTime: 30_000,
  })
  const memories = useQuery({
    queryKey: ['palette-memories', projectId, searchQ],
    enabled: open && !!projectId && searchQ.length >= 2,
    queryFn: ({ signal }) => memoryApi.search({ projectId: projectId!, q: searchQ, limit: 5 }, signal),
    staleTime: 30_000,
  })

  const items = useMemo(() => {
    const go = (to: string) => () => navigate(to)
    const command = (c: AppCommand) => () => runAppCommand(c)
    const theme = resolveTheme(getThemePreference())
    const actions: Item[] = [
      ...(DESKTOP_RELAY ? [{ id: 'new-chat', label: 'New chat', hint: 'Start a conversation with an agent on your computer', icon: Plus, group: 'Actions' as const, run: onChat || location.pathname.endsWith('/app/chats') ? command('new-chat') : go('/app/chats?new=1') }] : []),
      ...(onChat
        ? [
            { id: 'find', label: 'Find in this chat', hint: 'Search every message in the open chat', icon: Search, group: 'Actions' as const, keys: ['Mod', 'F'], run: command('find-in-chat') },
            { id: 'stop', label: 'Stop the reply', hint: 'Stops the agent; finished steps stay', icon: Square, group: 'Actions' as const, keys: ['Esc'], run: command('stop-reply') },
          ]
        : []),
      { id: 'theme', label: theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme', hint: 'Appearance', icon: theme === 'dark' ? Sun : Moon, group: 'Actions', run: () => setThemePreference(theme === 'dark' ? 'light' : 'dark') },
      { id: 'shortcuts', label: 'Keyboard shortcuts', hint: 'Every shortcut in one place', icon: Keyboard, group: 'Actions', keys: ['Mod', '/'], run: () => onShortcuts?.() },
    ]
    const seen = new Set<string>()
    const chatItem = (c: ChatCard): Item => ({
      id: `chat-${c.session_id}`,
      label: prefs.titles[c.session_id] || chatTitle(c.title, c.summary, c.harness),
      hint: `${agentName(c.harness)} · ${formatRelative(c.last_active_at)}`,
      icon: MessageSquare,
      group: 'Chats',
      run: go(`/app/chats?chat=${encodeURIComponent(c.session_id)}`),
    })
    const localChats = (q
      ? cachedChats.filter((c) => matches(q, prefs.titles[c.session_id], c.title, c.summary, agentName(c.harness)))
      : [...cachedChats.filter((c) => prefs.pinned.includes(c.session_id)), ...cachedChats]
    ).filter((c) => !seen.has(c.session_id) && seen.add(c.session_id))
    const remoteChats = q ? (serverChats.data?.items ?? []).filter((c) => !seen.has(c.session_id) && seen.add(c.session_id)) : []
    const chats = [...localChats, ...remoteChats].slice(0, q ? 6 : 5).map(chatItem)
    const mems: Item[] = q
      ? (memories.data?.items ?? []).slice(0, 5).map((m) => {
          const name = memoryTitle(m.key)
          return {
            id: `mem-${m.id}`,
            label: name.title,
            hint: (m.content ?? '').slice(0, 90),
            icon: Brain,
            group: 'Memories' as const,
            run: go(`/app/memory?q=${encodeURIComponent(m.key)}`),
          }
        })
      : []
    const search: Item[] = q
      ? [
          { id: 'search-chats', label: `Search chats for “${raw}”`, hint: 'Every chat in this project', icon: MessageSquare, group: 'Search', run: go(`/app/chats?q=${encodeURIComponent(raw)}`) },
          { id: 'search-memory', label: `Search memory for “${raw}”`, hint: 'What your agents learned', icon: Brain, group: 'Search', run: go(`/app/memory?q=${encodeURIComponent(raw)}`) },
        ]
      : []
    const goTo: Item[] = pages
      .filter((p) => matches(q, p.label, p.hint))
      .map((p) => ({ ...p, group: 'Go to' as const, run: go(p.to) }))
    const all = [...actions.filter((a) => matches(q, a.label, a.hint)), ...chats, ...mems, ...search, ...goTo]
    return GROUP_ORDER.flatMap((g) => all.filter((i) => i.group === g))
  }, [q, raw, cachedChats, serverChats.data, memories.data, prefs, onChat, location.pathname, navigate, onShortcuts])

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
    list.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [active])

  const pick = (item: Item | undefined) => {
    if (!item) return
    onClose()
    item.run()
  }

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        onClose()
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setActive((i) => (items.length ? (i + 1) % items.length : 0))
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setActive((i) => (items.length ? (i - 1 + items.length) % items.length : 0))
      }
      if (e.key === 'Enter' && items[active]) {
        e.preventDefault()
        pick(items[active])
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, items, active, onClose])

  const loading = searchQ.length >= 2 && (serverChats.isFetching || memories.isFetching)
  let lastGroup: Group | null = null

  return (
    <AnimatePresence>
      {open ? (
        <motion.div
          className="fixed inset-0 z-[70] flex items-start justify-center px-4 pt-[14vh]"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: reduced ? 0 : 0.12 }}
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
            initial={reduced ? false : { opacity: 0, y: -4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={reduced ? { opacity: 0 } : { opacity: 0, y: -4 }}
            transition={{ duration: reduced ? 0 : 0.12, ease: [0.1, 0.9, 0.2, 1] }}
            className="relative w-full max-w-[560px] overflow-hidden rounded-xl border border-border bg-surface shadow-[var(--shadow-glass)]"
          >
            <div className="flex items-center gap-3 border-b border-border px-4 py-3">
              <Search size={16} className="text-muted" />
              <input
                autoFocus
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Search chats, memories and actions…"
                aria-label="Search chats, memories and actions"
                role="combobox"
                aria-expanded="true"
                aria-controls="palette-results"
                aria-activedescendant={items[active] ? `palette-${items[active].id}` : undefined}
                className="w-full bg-transparent text-sm text-fg outline-none placeholder:text-muted"
              />
              {loading && <span className="palette-spinner" aria-hidden />}
              <kbd className="font-mono hidden rounded border border-border px-1.5 py-0.5 text-[10px] text-muted sm:inline">
                esc
              </kbd>
            </div>
            <ul id="palette-results" ref={list} role="listbox" aria-label="Results" className="max-h-[min(420px,60vh)] overflow-auto p-2">
              {items.length === 0 ? (
                <li className="px-3 py-6 text-center text-sm text-muted">{loading ? 'Searching…' : 'No matches'}</li>
              ) : (
                items.map((item, i) => {
                  const Icon = item.icon
                  const header = item.group !== lastGroup ? item.group : null
                  lastGroup = item.group
                  return (
                    <li key={item.id} role="presentation">
                      {header && (
                        <div className="px-3 pb-1 pt-2 text-[11px] font-semibold uppercase tracking-wide text-muted" role="presentation">
                          {header}
                        </div>
                      )}
                      <div
                        id={`palette-${item.id}`}
                        role="option"
                        aria-selected={i === active}
                        data-index={i}
                        onMouseMove={() => setActive(i)}
                        onClick={() => pick(item)}
                        className={[
                          'flex w-full cursor-pointer items-center gap-3 rounded-lg px-3 py-2 text-left transition-colors duration-100',
                          i === active ? 'bg-raised' : 'hover:bg-raised/60',
                        ].join(' ')}
                      >
                        <span className="flex h-8 w-8 flex-none items-center justify-center rounded-md border border-border bg-base text-fg-dim">
                          <Icon size={14} />
                        </span>
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-sm font-medium text-fg">{item.label}</span>
                          <span className="block truncate text-xs text-muted">{item.hint}</span>
                        </span>
                        {item.keys && (
                          <kbd className="font-mono hidden flex-none rounded border border-border px-1.5 py-0.5 text-[10px] text-muted sm:inline">
                            {comboLabel(item.keys)}
                          </kbd>
                        )}
                      </div>
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

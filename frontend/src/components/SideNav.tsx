import { AnimatePresence, motion } from 'framer-motion'
import {
  Activity,
  Bot,
  Building2,
  ChevronRight,
  Cable,
  LayoutDashboard,
  Layers,
  Menu,
  MessageSquare,
  Search,
  Shield,
  Split,
  Users,
  X,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { NotificationBell } from './NotificationBell'
import { Button } from './ui/Button'
import { ProjectSwitcher } from './ProjectSwitcher'
import { useAuth } from '@/providers/AuthProvider'
import { useMe } from '@/hooks/useAuthMutations'
import { useProjectPresence } from '@/hooks/useTeam'

const links = [
  { to: '/app/dashboard', label: 'Home', icon: LayoutDashboard },
  { to: '/app/memory', label: 'Memory', icon: Layers },
  { to: '/app/agents', label: 'Agents', icon: Bot },
  { to: '/app/sessions', label: 'Sessions', icon: MessageSquare },
  { to: '/app/team', label: 'Team', icon: Users },
  { to: '/app/org', label: 'Org', icon: Building2 },
  { to: '/app/activity', label: 'Activity', icon: Activity },
  // Memory CoW overlays — not git/GitHub (GitHub lives under Team).
  { to: '/app/branches', label: 'Overlays', icon: Split },
  { to: '/app/connect', label: 'Desktop', icon: Cable },
] as const

const AVATAR_COLORS = ['#5c5346', '#4a5560', '#5a4a3a', '#3d5348', '#53485c', '#4a5340']

function initialsFor(id: string, label?: string) {
  const fromLabel = (label ?? '').trim()
  if (fromLabel) return fromLabel.slice(0, 2).toUpperCase()
  const s = id.replace(/^user_/, '').slice(0, 2)
  return (s || '?').toUpperCase()
}

function colorFor(id: string) {
  let h = 0
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0
  return AVATAR_COLORS[h % AVATAR_COLORS.length]
}

type Props = {
  onOpenCommand: () => void
}

function NavLinks({ onNavigate }: { onNavigate?: () => void }) {
  const me = useMe()
  const admin = Boolean(me.data?.is_platform_admin)
  const items = [
    ...links,
    ...(admin ? ([{ to: '/app/admin', label: 'Platform', icon: Shield }] as const) : []),
  ]

  return (
    <nav className="app-nav">
      {items.map((link) => {
        const Icon = link.icon
        return (
          <NavLink
            key={link.to}
            to={link.to}
            onClick={onNavigate}
            className={({ isActive }) =>
              ['app-nav-link', isActive ? 'is-active' : ''].filter(Boolean).join(' ')
            }
          >
            <Icon size={15} strokeWidth={1.75} />
            {link.label}
          </NavLink>
        )
      })}
    </nav>
  )
}

function ProfileCard() {
  const { user, logout } = useAuth()
  const navigate = useNavigate()
  const initial = (user?.username?.[0] ?? 'U').toUpperCase()

  return (
    <div className="app-profile-wrap">
      <Link to="/app/settings" className="app-profile">
        <span className="app-profile-avatar" aria-hidden>
          {initial}
        </span>
        <span className="app-profile-meta">
          <span className="app-profile-name">{user?.username ?? 'User'}</span>
          <span className="app-profile-email">{user?.userId?.slice(0, 8) ?? '—'}…</span>
        </span>
        <ChevronRight size={14} className="app-profile-chevron" />
      </Link>
      <button
        type="button"
        className="app-logout"
        onClick={() => {
          logout()
          navigate('/login')
        }}
      >
        Log out
      </button>
    </div>
  )
}

function Presence() {
  const { user } = useAuth()
  const presence = useProjectPresence()

  const people = useMemo(() => {
    const map = new Map<string, { id: string; label: string; status: string }>()
    const add = (id: string, status: string, label?: string) => {
      const uid = id.trim()
      if (!uid) return
      const prev = map.get(uid)
      map.set(uid, {
        id: uid,
        status: status || prev?.status || 'online',
        label: (label || prev?.label || uid).trim(),
      })
    }
    if (user?.userId) add(user.userId, 'online', user.username)
    for (const p of presence.data ?? []) {
      add(p.user_id, p.status)
    }
    return [...map.values()]
  }, [presence.data, user])

  const visible = people.slice(0, 5)
  const extra = people.length - visible.length

  return (
    <div className="app-presence">
      <p className="app-presence-label">
        Online{people.length ? ` · ${people.length}` : ''}
      </p>
      <div className="app-avatars">
        {visible.map((p) => (
          <span
            key={p.id}
            className="app-avatar"
            style={{ backgroundColor: colorFor(p.id) }}
            title={`${p.label} · ${p.status}`}
          >
            {initialsFor(p.id, p.label)}
          </span>
        ))}
        {extra > 0 ? (
          <span className="app-avatar" title={`${extra} more online`}>
            +{extra}
          </span>
        ) : null}
      </div>
    </div>
  )
}

export function SideNav({ onOpenCommand }: Props) {
  return (
    <aside className="app-sidebar" aria-label="Main">
      <NavLink to="/app/dashboard" className="app-brand">
        Nexus
      </NavLink>

      <div className="mt-5 flex items-center gap-1">
        <button type="button" className="app-search !mt-0 !w-auto min-w-0 flex-1" onClick={onOpenCommand}>
          <Search size={13} />
          <span className="app-search-label">Search</span>
          <kbd className="app-search-kbd">⌘K</kbd>
        </button>
        <NotificationBell />
      </div>

      <div className="mt-3">
        <ProjectSwitcher />
      </div>

      <NavLinks />
      <Presence />
      <ProfileCard />
    </aside>
  )
}

export function MobileNav({ onOpenCommand }: Props) {
  const [open, setOpen] = useState(false)

  return (
    <>
      <div className="app-mobile-bar">
        <span className="app-brand">Nexus</span>
        <div className="app-mobile-actions">
          <NotificationBell />
          <Button variant="ghost" size="sm" onClick={onOpenCommand} aria-label="Search">
            <Search size={16} />
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setOpen(true)} aria-label="Open menu">
            <Menu size={16} />
          </Button>
        </div>
      </div>

      <AnimatePresence>
        {open ? (
          <motion.div
            className="app-drawer-root"
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
          >
            <button
              type="button"
              className="app-drawer-backdrop"
              aria-label="Close menu"
              onClick={() => setOpen(false)}
            />
            <motion.aside
              className="app-drawer"
              initial={{ x: '-100%' }}
              animate={{ x: 0 }}
              exit={{ x: '-100%' }}
              transition={{ type: 'spring', stiffness: 380, damping: 34 }}
            >
              <div className="app-drawer-head">
                <span className="app-brand">Nexus</span>
                <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>
                  <X size={16} />
                </Button>
              </div>

              <button
                type="button"
                className="app-search"
                onClick={() => {
                  setOpen(false)
                  onOpenCommand()
                }}
              >
                <Search size={13} />
                <span className="app-search-label">Search</span>
                <kbd className="app-search-kbd">⌘K</kbd>
              </button>

              <NavLinks onNavigate={() => setOpen(false)} />
              <Presence />
              <ProfileCard />
            </motion.aside>
          </motion.div>
        ) : null}
      </AnimatePresence>
    </>
  )
}

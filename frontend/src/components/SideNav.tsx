import { AnimatePresence, motion } from 'framer-motion'
import {
  Bot,
  ChevronRight,
  GitBranch,
  Layers,
  Menu,
  MessageSquare,
  Search,
  Settings,
  Users,
  X,
} from 'lucide-react'
import { useState } from 'react'
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { Button } from './ui/Button'
import { useAuth } from '@/providers/AuthProvider'

const links = [
  { to: '/app/memory', label: 'Memory', icon: Layers },
  { to: '/app/agents', label: 'Agents', icon: Bot },
  { to: '/app/team', label: 'Team', icon: Users },
  { to: '/app/branches', label: 'Branches', icon: GitBranch },
  { to: '/app/sessions', label: 'Sessions', icon: MessageSquare },
  { to: '/app/settings', label: 'Settings', icon: Settings },
] as const

const members = [
  { initials: 'A', name: 'Alice', color: '#5c5346' },
  { initials: 'B', name: 'Bob', color: '#4a5560' },
  { initials: 'C', name: 'Chris', color: '#5a4a3a' },
]

type Props = {
  onOpenCommand: () => void
}

function NavLinks({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <nav className="app-nav">
      {links.map((link) => {
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
  return (
    <div className="app-presence">
      <p className="app-presence-label">Online</p>
      <div className="app-avatars">
        {members.map((m) => (
          <span
            key={m.initials}
            className="app-avatar"
            style={{ backgroundColor: m.color }}
            title={m.name}
          >
            {m.initials}
          </span>
        ))}
      </div>
    </div>
  )
}

export function SideNav({ onOpenCommand }: Props) {
  return (
    <aside className="app-sidebar" aria-label="Main">
      <NavLink to="/app/memory" className="app-brand">
        Nexus
      </NavLink>

      <button type="button" className="app-search" onClick={onOpenCommand}>
        <Search size={13} />
        <span className="app-search-label">Search</span>
        <kbd className="app-search-kbd">⌘K</kbd>
      </button>

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

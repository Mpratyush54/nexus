import { AnimatePresence, motion } from 'framer-motion'
import { ChevronRight, Menu, Search, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, NavLink, useLocation, useNavigate } from 'react-router-dom'
import { NotificationBell } from './NotificationBell'
import { NexusBrand } from './NexusBrand'
import { Button } from './ui/Button'
import { shortcutLabel } from '@/lib/shortcut'
import { isDemo } from '@/lib/diagnostics'
import { ProjectSwitcher } from './ProjectSwitcher'
import { useAuth } from '@/providers/AuthProvider'
import { useMe } from '@/hooks/useAuthMutations'
import { initialsOf, usePeople } from '@/hooks/usePeople'
import { useProjectPresence } from '@/hooks/useTeam'
import {
  FOOTER_ITEMS,
  MOBILE_TABS,
  NAV_SECTIONS,
  SETTINGS_ITEM,
  isNavActive,
  type NavItem,
} from '@/lib/navigation'

const AVATAR_COLORS = ['#5c5346', '#4a5560', '#5a4a3a', '#3d5348', '#53485c', '#4a5340']

function initialsFor(id: string, label?: string) {
  const fromLabel = (label ?? '').trim()
  if (fromLabel) return initialsOf(fromLabel)
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

function NavItemLink({ item, onNavigate }: { item: NavItem; onNavigate?: () => void }) {
  const { pathname } = useLocation()
  const Icon = item.icon
  const active = isNavActive(item, pathname)
  return (
    <NavLink
      to={item.to}
      title={item.label}
      onClick={onNavigate}
      aria-current={active ? 'page' : undefined}
      className={['app-nav-link', active ? 'is-active' : ''].filter(Boolean).join(' ')}
    >
      <Icon size={15} strokeWidth={1.75} aria-hidden />
      <span className="app-nav-text">{item.label}</span>
    </NavLink>
  )
}

function NavLinks({ onNavigate }: { onNavigate?: () => void }) {
  const me = useMe()
  const admin = Boolean(me.data?.is_platform_admin)
  const footer = FOOTER_ITEMS.filter((i) => !i.adminOnly || admin)

  return (
    <nav className="app-nav" aria-label="Sections">
      {NAV_SECTIONS.map((section) => (
        <div key={section.id} className="app-nav-section">
          {section.label ? <p className="app-nav-heading">{section.label}</p> : null}
          {section.items
            .filter((i) => !i.adminOnly || admin)
            .map((item) => (
              <NavItemLink key={item.to} item={item} onNavigate={onNavigate} />
            ))}
        </div>
      ))}
      <div className="app-nav-section app-nav-bottom">
        {footer.map((item) => (
          <NavItemLink key={item.to} item={item} onNavigate={onNavigate} />
        ))}
        <NavItemLink item={SETTINGS_ITEM} onNavigate={onNavigate} />
      </div>
    </nav>
  )
}

function ProfileCard() {
  const { user, logout } = useAuth()
  const me = useMe()
  const navigate = useNavigate()
  const initial = (user?.username?.[0] ?? 'U').toUpperCase()

  return (
    <div className="app-profile-wrap">
      <Link to="/app/settings" className="app-profile" title="Account settings">
        <span className="app-profile-avatar" aria-hidden>
          {initial}
        </span>
        <span className="app-profile-meta">
          <span className="app-profile-name">{user?.username ?? 'User'}</span>
          <span className="app-profile-email">{me.data?.email || 'Settings'}</span>
        </span>
        <ChevronRight size={14} className="app-profile-chevron" aria-hidden />
      </Link>
      {isDemo() ? (
        // The demo has no account to sign out of: this leaves it, like the banner does.
        <a className="app-logout" href="/">
          Exit demo
        </a>
      ) : (
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
      )}
    </div>
  )
}

function Presence() {
  const { user } = useAuth()
  const presence = useProjectPresence()
  const { nameFor } = usePeople()

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
      add(p.user_id, p.status, nameFor(p.user_id, { you: false }))
    }
    return [...map.values()]
  }, [presence.data, user, nameFor])

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
            // Light text: the avatar colours are dark in both themes.
            style={{ backgroundColor: colorFor(p.id), color: '#f7f4ec' }}
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
      <div className="app-sidebar-top">
        <NavLink to="/app/dashboard" className="app-brand">
          <NexusBrand />
        </NavLink>

        <div className="mt-5 flex items-center gap-1">
          <button
            type="button"
            className="app-search !mt-0 !w-auto min-w-0 flex-1"
            onClick={onOpenCommand}
            aria-keyshortcuts="Control+K Meta+K"
          >
            <Search size={13} />
            <span className="app-search-label">Search</span>
            <kbd className="app-search-kbd">{shortcutLabel()}</kbd>
          </button>
          <NotificationBell />
        </div>

        <div className="mt-3">
          <ProjectSwitcher />
        </div>
      </div>

      <NavLinks />

      <div className="app-sidebar-foot">
        <Presence />
        <ProfileCard />
      </div>
    </aside>
  )
}

export function MobileNav({ onOpenCommand }: Props) {
  const [open, setOpen] = useState(false)
  const menuButton = useRef<HTMLButtonElement>(null)
  const closeButton = useRef<HTMLButtonElement>(null)
  const { pathname } = useLocation()
  const moreActive = !MOBILE_TABS.some((t) => isNavActive(t, pathname))

  useEffect(() => {
    if (!open) return
    closeButton.current?.focus()
    const opener = menuButton.current
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
      opener?.focus()
    }
  }, [open])

  return (
    <>
      <div className="app-mobile-bar">
        <span className="app-brand"><NexusBrand /></span>
        <div className="app-mobile-actions">
          <NotificationBell />
          <Button variant="ghost" size="sm" onClick={onOpenCommand} aria-label="Search">
            <Search size={16} />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="app-menu-btn"
            onClick={() => setOpen(true)}
            aria-label="Open menu"
            aria-expanded={open}
            aria-controls="app-drawer"
          >
            <Menu size={16} />
          </Button>
        </div>
      </div>

      <nav className="app-tabbar" aria-label="Quick sections">
        {MOBILE_TABS.map((item) => {
          const Icon = item.icon
          const active = isNavActive(item, pathname)
          return (
            <NavLink
              key={item.to}
              to={item.to}
              aria-current={active ? 'page' : undefined}
              className={['app-tab', active ? 'is-active' : ''].filter(Boolean).join(' ')}
            >
              <Icon size={18} strokeWidth={1.75} aria-hidden />
              <span>{item.label}</span>
            </NavLink>
          )
        })}
        <button
          ref={menuButton}
          type="button"
          data-shot="more"
          className={['app-tab', moreActive ? 'is-active' : ''].filter(Boolean).join(' ')}
          onClick={() => setOpen(true)}
          aria-label="More sections"
          aria-expanded={open}
          aria-controls="app-drawer"
        >
          <Menu size={18} strokeWidth={1.75} aria-hidden />
          <span>More</span>
        </button>
      </nav>

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
              id="app-drawer"
              role="dialog"
              aria-modal="true"
              aria-label="Menu"
              className="app-drawer"
              initial={{ x: '-100%' }}
              animate={{ x: 0 }}
              exit={{ x: '-100%' }}
              transition={{ type: 'spring', stiffness: 380, damping: 34 }}
            >
              <div className="app-drawer-head">
                <span className="app-brand"><NexusBrand /></span>
                <Button ref={closeButton} variant="ghost" size="sm" onClick={() => setOpen(false)} aria-label="Close menu">
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
                <kbd className="app-search-kbd">{shortcutLabel()}</kbd>
              </button>

              <div className="mt-3 px-0.5">
                <ProjectSwitcher />
              </div>

              <div className="app-nav-scroll flex min-h-0 flex-1 flex-col overflow-y-auto">
                <NavLinks onNavigate={() => setOpen(false)} />
              </div>
              <div className="app-sidebar-foot mt-auto shrink-0 border-t border-border pt-3">
                <ProfileCard />
              </div>
            </motion.aside>
          </motion.div>
        ) : null}
      </AnimatePresence>
    </>
  )
}

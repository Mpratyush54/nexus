import {
  Activity,
  Cable,
  History,
  LayoutDashboard,
  Layers,
  Settings,
  Shield,
  Smartphone,
  Users,
  type LucideIcon,
} from 'lucide-react'

export type NavItem = {
  to: string
  label: string
  icon: LucideIcon
  /** Other routes that should light this item up (e.g. /app/org under Team). */
  match?: string[]
  adminOnly?: boolean
}

export type NavSection = {
  id: string
  label: string
  items: NavItem[]
}

/** Where "Steer from phone" points: the Phone & devices page. */
export const STEER_ROUTE = '/app/devices'

/**
 * Six places, no groups. Older pages were folded in and their routes redirect:
 * Agents → Activity, Memory overlays → Memory, Sessions and Teleport inbox →
 * Phone & devices.
 */
export const NAV_SECTIONS: NavSection[] = [
  {
    id: 'main',
    label: '',
    items: [
      { to: '/app/dashboard', label: 'Home', icon: LayoutDashboard },
      { to: '/app/chats', label: 'Chats', icon: History, match: ['/app/timeline'] },
      { to: '/app/memory', label: 'Memory', icon: Layers, match: ['/app/branches'] },
      { to: '/app/activity', label: 'Activity', icon: Activity, match: ['/app/agents'] },
      { to: STEER_ROUTE, label: 'Phone & devices', icon: Smartphone, match: ['/app/sessions', '/app/teleport'] },
      { to: '/app/team', label: 'Team', icon: Users, match: ['/app/org'] },
    ],
  },
]

/** Quieter links under the main list. */
export const FOOTER_ITEMS: NavItem[] = [
  { to: '/app/connect', label: 'Desktop app', icon: Cable, match: ['/app/onboarding'] },
  { to: '/app/admin', label: 'Platform', icon: Shield, adminOnly: true },
]

export const SETTINGS_ITEM: NavItem = { to: '/app/settings', label: 'Settings', icon: Settings }

/** Phone bottom bar: the four things people open most, plus "More". */
export const MOBILE_TABS: NavItem[] = [
  { to: '/app/dashboard', label: 'Home', icon: LayoutDashboard },
  { to: '/app/chats', label: 'Chats', icon: History, match: ['/app/timeline'] },
  { to: '/app/memory', label: 'Memory', icon: Layers, match: ['/app/branches'] },
  { to: STEER_ROUTE, label: 'Phone', icon: Smartphone, match: ['/app/sessions', '/app/teleport'] },
]

export function isNavActive(item: NavItem, pathname: string): boolean {
  const paths = [item.to, ...(item.match ?? [])]
  return paths.some((p) => pathname === p || pathname.startsWith(`${p}/`))
}

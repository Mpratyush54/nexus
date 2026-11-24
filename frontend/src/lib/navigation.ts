import {
  Activity,
  Bot,
  Cable,
  History,
  LayoutDashboard,
  Layers,
  Send,
  Settings,
  Shield,
  Smartphone,
  Split,
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

/**
 * Where "Steer from phone" points. The steering UI lives on the Sessions page
 * today; swap this when a dedicated phone-steer route lands.
 */
export const STEER_ROUTE = '/app/sessions'

export const NAV_SECTIONS: NavSection[] = [
  {
    id: 'work',
    label: 'Work',
    items: [
      { to: '/app/dashboard', label: 'Home', icon: LayoutDashboard },
      { to: '/app/chats', label: 'Chats', icon: History, match: ['/app/timeline'] },
      { to: '/app/memory', label: 'Memory', icon: Layers },
      { to: '/app/team', label: 'Team & org', icon: Users, match: ['/app/org'] },
    ],
  },
  {
    id: 'devices',
    label: 'Phone & devices',
    items: [
      { to: STEER_ROUTE, label: 'Steer from phone', icon: Smartphone },
      { to: '/app/teleport', label: 'Teleport inbox', icon: Send },
      { to: '/app/connect', label: 'Desktop app', icon: Cable },
    ],
  },
  {
    id: 'more',
    label: 'Advanced',
    items: [
      { to: '/app/activity', label: 'Activity', icon: Activity },
      { to: '/app/agents', label: 'Agents', icon: Bot },
      { to: '/app/branches', label: 'Memory overlays', icon: Split },
      { to: '/app/admin', label: 'Platform', icon: Shield, adminOnly: true },
    ],
  },
]

export const SETTINGS_ITEM: NavItem = { to: '/app/settings', label: 'Settings', icon: Settings }

/** Phone bottom bar: the four things people open most, plus "More". */
export const MOBILE_TABS: NavItem[] = [
  { to: '/app/dashboard', label: 'Home', icon: LayoutDashboard },
  { to: '/app/chats', label: 'Chats', icon: History, match: ['/app/timeline'] },
  { to: '/app/memory', label: 'Memory', icon: Layers },
  { to: STEER_ROUTE, label: 'Steer', icon: Smartphone },
]

export function isNavActive(item: NavItem, pathname: string): boolean {
  const paths = [item.to, ...(item.match ?? [])]
  return paths.some((p) => pathname === p || pathname.startsWith(`${p}/`))
}

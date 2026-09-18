import { Bell } from 'lucide-react'
import { NavLink, useLocation } from 'react-router-dom'
import { unreadNotifications } from '@/api/notifications'
import { useNotifications } from '@/hooks/useNotifications'
import { useAuth } from '@/providers/AuthProvider'

export function NotificationBell({ className = '' }: { className?: string }) {
  const { projectId } = useAuth()
  const location = useLocation()
  const inbox = useNotifications()
  const onInbox = location.pathname.startsWith('/app/notifications')
  const unread = onInbox ? 0 : projectId ? unreadNotifications(inbox.data ?? [], projectId) : 0

  return (
    <NavLink
      to="/app/notifications"
      className={[
        'relative inline-flex h-8 w-8 items-center justify-center rounded-lg text-fg-dim hover:bg-raised hover:text-fg',
        className,
      ].join(' ')}
      aria-label={unread ? `${unread} unread notifications` : 'Notifications'}
    >
      <Bell size={15} strokeWidth={1.75} />
      {unread > 0 ? (
        <span className="absolute right-1 top-1 h-1.5 w-1.5 rounded-full bg-amber" />
      ) : null}
    </NavLink>
  )
}

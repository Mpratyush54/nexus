import { Navigate, Outlet, useLocation, useSearchParams } from 'react-router-dom'
import { useAuth } from '@/providers/AuthProvider'
import { useNexusSocket } from '@/hooks/useNexusSocket'
import { guestRedirect, useHandoff } from '@/auth/handoff'

export function ProtectedRoute() {
  const { isAuthenticated } = useAuth()
  const location = useLocation()
  useNexusSocket()

  if (!isAuthenticated) {
    // Come back to the page you asked for after signing in.
    const next = `${location.pathname}${location.search}${location.hash}`
    return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace state={{ from: location.pathname }} />
  }

  return <Outlet />
}

export function GuestRoute() {
  const { isAuthenticated } = useAuth()
  const [params] = useSearchParams()
  const next = params.get('next')
  const handoff = useHandoff()
  // A sign-in that just succeeded on this page picks its own destination and
  // plays the hand-off; don't yank the page away underneath it.
  const to = guestRedirect(isAuthenticated, handoff, next)
  return to ? <Navigate to={to} replace /> : <Outlet />
}

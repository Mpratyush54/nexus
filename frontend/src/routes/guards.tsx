import { Navigate, Outlet, useLocation, useSearchParams } from 'react-router-dom'
import { useAuth } from '@/providers/AuthProvider'
import { useNexusSocket } from '@/hooks/useNexusSocket'
import { useHandoff } from '@/auth/handoff'

export function ProtectedRoute() {
  const { isAuthenticated } = useAuth()
  const location = useLocation()
  useNexusSocket()

  if (!isAuthenticated) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />
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
  if (isAuthenticated && handoff === 'idle') {
    if (next && next.startsWith('/cli/')) {
      return <Navigate to={next} replace />
    }
    return <Navigate to="/app/connect" replace />
  }
  return <Outlet />
}

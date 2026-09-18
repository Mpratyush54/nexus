import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { useAuth } from '@/providers/AuthProvider'
import { useNexusSocket } from '@/hooks/useNexusSocket'

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
  if (isAuthenticated) {
    return <Navigate to="/app/memory" replace />
  }
  return <Outlet />
}

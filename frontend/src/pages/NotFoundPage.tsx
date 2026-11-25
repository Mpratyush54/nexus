import { Link, useLocation } from 'react-router-dom'
import { EdgeFrame } from '@/auth/edge'
import { useAuth } from '@/providers/AuthProvider'

export function NotFoundPage() {
  const { isAuthenticated } = useAuth()
  const { pathname } = useLocation()
  return (
    <EdgeFrame>
      <p className="au-edge-code" aria-hidden>
        4<mark>0</mark>4
      </p>
      <h1 className="au-title" style={{ marginTop: 18 }}>
        This page isn’t here
      </h1>
      <p className="au-sub">The link may be old or mistyped. Nothing on your account was affected.</p>
      <code className="au-edge-path">{pathname}</code>
      <div className="au-edge-actions">
        <a className="au-btn" href="/">
          Go to Nexus home
        </a>
        {isAuthenticated ? (
          <Link className="au-btn is-secondary" to="/app/dashboard">
            Open your workspace
          </Link>
        ) : (
          <Link className="au-btn is-secondary" to="/login">
            Sign in
          </Link>
        )}
      </div>
    </EdgeFrame>
  )
}

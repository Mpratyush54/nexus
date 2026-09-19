import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { authApi } from '@/api/auth'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { useAuth } from '@/providers/AuthProvider'

function isLoopbackRedirect(raw: string): boolean {
  try {
    const u = new URL(raw)
    if (u.protocol !== 'http:') return false
    if (u.hostname !== '127.0.0.1' && u.hostname !== 'localhost') return false
    if (u.pathname !== '/callback' && u.pathname !== '/callback/') return false
    return true
  } catch {
    return false
  }
}

/** Desktop app OAuth-style bridge: web login → localhost callback with token. */
export function CliAuthPage() {
  const { isAuthenticated, token, user } = useAuth()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const [error, setError] = useState<string | null>(null)
  const [phase, setPhase] = useState<'check' | 'mint' | 'redirect' | 'done'>('check')

  const redirect = useMemo(() => params.get('redirect')?.trim() ?? '', [params])
  const state = useMemo(() => params.get('state')?.trim() ?? '', [params])
  const nextPath = useMemo(() => {
    const q = new URLSearchParams()
    if (redirect) q.set('redirect', redirect)
    if (state) q.set('state', state)
    return `/cli/auth?${q.toString()}`
  }, [redirect, state])

  useEffect(() => {
    if (!redirect || !isLoopbackRedirect(redirect)) {
      setError('Invalid desktop redirect URL. Expected http://127.0.0.1:<port>/callback')
      return
    }
    if (!isAuthenticated || !token || !user) {
      navigate(`/login?next=${encodeURIComponent(nextPath)}`, { replace: true })
      return
    }

    let cancelled = false
    ;(async () => {
      setPhase('mint')
      let desktopToken = token
      try {
        const minted = await authApi.createToken({
          name: `Nexus Desktop (${new Date().toISOString().slice(0, 10)})`,
          scopes: ['*'],
        })
        if (minted.token) desktopToken = minted.token
      } catch {
        // Fall back to session JWT if API tokens are unavailable.
      }
      if (cancelled) return
      setPhase('redirect')
      const target = new URL(redirect)
      target.searchParams.set('token', desktopToken)
      target.searchParams.set('user_id', user.userId)
      target.searchParams.set('username', user.username)
      if (state) target.searchParams.set('state', state)
      setPhase('done')
      window.location.replace(target.toString())
    })()

    return () => {
      cancelled = true
    }
  }, [isAuthenticated, token, user, redirect, state, nextPath, navigate])

  return (
    <div className="mesh-bg flex min-h-svh items-center justify-center px-4 py-12">
      <div className="w-full max-w-md">
        <Link to="/" className="mb-8 block text-center text-lg font-semibold tracking-tight text-fg">
          Nexus
        </Link>
        <GlassPanel className="p-6 sm:p-7">
          <h1 className="text-xl font-semibold text-fg">Connect desktop app</h1>
          {error ? (
            <p className="mt-3 text-sm text-[var(--color-danger)]">{error}</p>
          ) : (
            <p className="mt-3 text-sm text-fg-dim">
              {phase === 'mint' && 'Creating a desktop access token…'}
              {phase === 'redirect' && 'Sending credentials back to Nexus Desktop…'}
              {phase === 'done' && 'You can close this tab.'}
              {phase === 'check' && 'Checking session…'}
            </p>
          )}
        </GlassPanel>
      </div>
    </div>
  )
}

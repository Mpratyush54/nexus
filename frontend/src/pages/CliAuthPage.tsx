import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { authApi } from '@/api/auth'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { useAuth } from '@/providers/AuthProvider'

function isDesktopRedirect(raw: string): boolean {
  try {
    const u = new URL(raw)
    if (u.protocol === 'nexus:') {
      return u.hostname === 'auth' && (u.pathname === '/callback' || u.pathname === '/callback/')
    }
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
    if (!redirect || !isDesktopRedirect(redirect)) {
      setError('Invalid desktop redirect URL. Expected nexus://auth/callback or a loopback callback.')
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
      const payload = new URLSearchParams({
        token: desktopToken,
        user_id: user.userId,
        username: user.username,
      })
      if (state) payload.set('state', state)
      if (target.protocol === 'nexus:') {
        target.hash = payload.toString()
      } else {
        payload.forEach((value, key) => target.searchParams.set(key, value))
      }
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

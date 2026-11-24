import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { authApi } from '@/api/auth'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { Button } from '@/components/ui/Button'
import { NexusBrand } from '@/components/NexusBrand'
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
  const [phase, setPhase] = useState<'check' | 'confirm' | 'mint' | 'redirect' | 'done'>('check')
  const [approved, setApproved] = useState(false)

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
      setError(
        "This sign-in link isn't from Nexus Desktop or has expired. Start sign-in again from the desktop app.",
      )
      return
    }
    if (!isAuthenticated || !token || !user) {
      navigate(`/login?next=${encodeURIComponent(nextPath)}`, { replace: true })
      return
    }
    // Ask before handing a token to a local app: a link alone must not do it.
    if (!approved) {
      setPhase('confirm')
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
  }, [isAuthenticated, token, user, redirect, state, nextPath, navigate, approved])

  return (
    <div className="mesh-bg flex min-h-svh items-center justify-center px-4 py-12">
      <div className="w-full max-w-md">
        <Link to="/" className="mb-8 flex justify-center text-fg">
          <NexusBrand />
        </Link>
        <GlassPanel className="p-6 sm:p-7">
          <h1 className="text-xl font-semibold text-fg">Connect Nexus Desktop</h1>
          {error ? (
            <>
              <p className="mt-3 text-sm text-danger" role="alert">{error}</p>
              <Link
                to={isAuthenticated ? '/app/connect' : '/login'}
                className="mt-5 inline-flex text-sm text-amber underline-offset-2 hover:underline"
              >
                {isAuthenticated ? 'Go to Desktop setup' : 'Log in to Nexus'}
              </Link>
            </>
          ) : phase === 'confirm' ? (
            <>
              <p className="mt-3 text-sm text-fg-dim">
                Nexus Desktop on this computer wants to sign in as{' '}
                <strong className="text-fg">{user?.username}</strong>. It will be able to sync
                your chats and memory.
              </p>
              <div className="mt-5 flex flex-wrap gap-2">
                <Button onClick={() => setApproved(true)}>Connect Desktop</Button>
                <Button variant="secondary" onClick={() => navigate('/app/dashboard', { replace: true })}>
                  Cancel
                </Button>
              </div>
              <p className="mt-4 text-xs text-muted">
                Didn't start this from Nexus Desktop? Cancel. Nothing is shared.
              </p>
            </>
          ) : (
            <p className="mt-3 text-sm text-fg-dim" role="status">
              {phase === 'mint' && 'Creating a desktop access token…'}
              {phase === 'redirect' && 'Returning to Nexus Desktop…'}
              {phase === 'done' && 'Done. You can close this tab.'}
              {phase === 'check' && 'Checking your session…'}
            </p>
          )}
        </GlassPanel>
      </div>
    </div>
  )
}

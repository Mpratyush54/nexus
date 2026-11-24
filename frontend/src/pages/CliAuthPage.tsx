import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { Check, CircleSlash, Laptop, Link2Off, ShieldCheck } from 'lucide-react'
import { authApi } from '@/api/auth'
import { AuthLayout } from '@/auth/AuthLayout'
import { SubmitButton } from '@/auth/fields'
import { StepSwap } from '@/auth/StepSwap'
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

/**
 * What we can honestly say about the requesting device. Nexus Desktop only
 * sends a callback address and a state value, so the rest comes from this
 * browser, which runs on the same machine as the app that opened it.
 */
function describeDevice(redirect: string) {
  const ua = typeof navigator !== 'undefined' ? navigator.userAgent : ''
  const os = /Windows/i.test(ua)
    ? 'Windows'
    : /Mac OS X|Macintosh/i.test(ua)
      ? 'macOS'
      : /Android/i.test(ua)
        ? 'Android'
        : /iPhone|iPad/i.test(ua)
          ? 'iOS'
          : /Linux/i.test(ua)
            ? 'Linux'
            : 'this computer'
  let via = 'Nexus app link'
  try {
    const u = new URL(redirect)
    if (u.protocol === 'http:') via = `local sign-in on port ${u.port || '80'}`
  } catch {
    /* validated elsewhere */
  }
  return { os, via }
}

type Phase = 'check' | 'confirm' | 'mint' | 'redirect' | 'done' | 'denied'

/** Desktop app OAuth-style bridge: web login → localhost callback with token. */
export function CliAuthPage() {
  const { isAuthenticated, token, user } = useAuth()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const [error, setError] = useState<string | null>(null)
  const [phase, setPhase] = useState<Phase>('check')
  const [approved, setApproved] = useState(false)

  const redirect = useMemo(() => params.get('redirect')?.trim() ?? '', [params])
  const state = useMemo(() => params.get('state')?.trim() ?? '', [params])
  const nextPath = useMemo(() => {
    const q = new URLSearchParams()
    if (redirect) q.set('redirect', redirect)
    if (state) q.set('state', state)
    return `/cli/auth?${q.toString()}`
  }, [redirect, state])
  const device = useMemo(() => describeDevice(redirect), [redirect])

  useEffect(() => {
    if (!redirect || !isDesktopRedirect(redirect)) {
      setError("This sign-in link isn't from Nexus Desktop or has expired. Start sign-in again from the desktop app.")
      return
    }
    if (!isAuthenticated || !token || !user) {
      navigate(`/login?next=${encodeURIComponent(nextPath)}`, { replace: true })
      return
    }
    // Ask before handing a token to a local app: a link alone must not do it.
    if (!approved) {
      setPhase((p) => (p === 'denied' ? p : 'confirm'))
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

  // The tab has done its job. Browsers only let a script close a tab that a
  // script opened, so this quietly does nothing when the user opened it.
  useEffect(() => {
    if (phase !== 'done') return
    const t = window.setTimeout(() => window.close(), 2500)
    return () => window.clearTimeout(t)
  }, [phase])

  const signedInAs = user ? (
    <span className="au-top-link">
      Signed in as <strong>{user.username}</strong>
    </span>
  ) : (
    <span />
  )

  let body
  if (error) {
    body = (
      <div className="au-done">
        <div className="au-done-icon is-error">
          <Link2Off size={24} aria-hidden />
        </div>
        <p className="au-kicker">nexus desktop</p>
        <h1 className="au-title">This sign-in link doesn’t work</h1>
        <p className="au-sub" role="alert">
          {error}
        </p>
        <div className="au-edge-actions">
          <Link className="au-btn" to={isAuthenticated ? '/app/connect' : '/login'}>
            {isAuthenticated ? 'Open Desktop setup' : 'Log in to Nexus'}
          </Link>
        </div>
      </div>
    )
  } else if (phase === 'denied') {
    body = (
      <div className="au-done">
        <div className="au-done-icon is-error">
          <CircleSlash size={24} aria-hidden />
        </div>
        <p className="au-kicker">nexus desktop · denied</p>
        <h1 className="au-title">Nothing was shared</h1>
        <p className="au-sub">
          Nexus Desktop was not signed in. You can close this tab. If you meant to connect, start
          sign-in again from the desktop app.
        </p>
        <div className="au-edge-actions">
          <Link className="au-btn is-secondary" to="/app/dashboard">
            Go to the web app
          </Link>
        </div>
      </div>
    )
  } else if (phase === 'done' || phase === 'redirect') {
    body = (
      <div className="au-done" role="status">
        <div className="au-done-icon">
          <Check size={26} aria-hidden />
        </div>
        <p className="au-kicker">nexus desktop · connected</p>
        <h1 className="au-title">You can return to the app</h1>
        <p className="au-sub">
          Nexus Desktop is signed in as <strong>{user?.username}</strong>. This tab will try to close
          itself; if it stays open, you can close it.
        </p>
        <p className="au-note">Didn’t switch back? Open Nexus from the Start menu or the tray icon.</p>
      </div>
    )
  } else if (phase === 'confirm' || phase === 'mint') {
    body = (
      <div>
        <div className="au-head">
          <p className="au-kicker">nexus desktop · approve</p>
          <h1 className="au-title">Authorize Nexus Desktop on this device?</h1>
          <p className="au-sub">
            The desktop app will sign in as <strong>{user?.username}</strong>.
          </p>
        </div>
        <div className="au-device">
          <span className="au-device-icon">
            <Laptop size={22} aria-hidden />
          </span>
          <span className="au-device-name">Nexus Desktop on {device.os}</span>
          <span className="au-device-meta">via {device.via}</span>
        </div>
        <ul className="au-scopes">
          <li>
            <ShieldCheck size={16} aria-hidden /> Sync your chats, sessions and project memory with your
            account
          </li>
          <li>
            <ShieldCheck size={16} aria-hidden /> Get its own access token, which you can revoke anytime in
            Settings
          </li>
        </ul>
        <div className="au-actions">
          <SubmitButton type="button" busy={phase === 'mint'} busyLabel="Authorizing…" onClick={() => setApproved(true)}>
            Authorize
          </SubmitButton>
          <button type="button" className="au-btn is-secondary" disabled={phase === 'mint'} onClick={() => setPhase('denied')}>
            Deny
          </button>
        </div>
        <p className="au-note">Didn’t start this from Nexus Desktop? Choose Deny. Nothing is shared.</p>
      </div>
    )
  } else {
    body = (
      <div role="status" style={{ minHeight: 200, display: 'grid', placeItems: 'center' }}>
        <span className="au-sub" style={{ display: 'inline-flex', alignItems: 'center', gap: 10 }}>
          <span className="au-spinner" aria-hidden /> Checking your session…
        </span>
      </div>
    )
  }

  const key = error ? 'error' : phase === 'mint' ? 'confirm' : phase === 'redirect' ? 'done' : phase
  return (
    <AuthLayout topAction={signedInAs}>
      <StepSwap step={key}>{body}</StepSwap>
    </AuthLayout>
  )
}

import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { Sparkles } from 'lucide-react'
import { useLogin } from '@/hooks/useAuthMutations'
import { useIsDemo } from '@/auth/demo'
import { friendlyAuthError, type FriendlyError } from '@/auth/errors'
import { Field, FormAlert, PasswordField, SubmitButton } from '@/auth/fields'
import { afterAuthPath, handoff, prefetchApp, useReleaseHoldOnLeave } from '@/auth/handoff'

export function LoginPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const login = useLogin()
  const demo = useIsDemo()
  const [identifier, setIdentifier] = useState('')
  const [password, setPassword] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const [error, setError] = useState<FriendlyError | null>(null)
  const [done, setDone] = useState(false)
  const next = params.get('next')
  const forDesktop = Boolean(next?.startsWith('/cli/'))
  const keep = next ? `?next=${encodeURIComponent(next)}` : ''
  useReleaseHoldOnLeave()

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    const user = identifier.trim()
    if (!user || !password || login.isPending || done) return
    setError(null)
    handoff.hold()
    prefetchApp()
    login.mutate(
      { username: user, password },
      {
        onSuccess: () => {
          setDone(true)
          handoff.cover()
          // Let the cover fade in before the route changes underneath it.
          window.setTimeout(() => navigate(afterAuthPath(next), { replace: true }), 240)
        },
        onError: (err) => {
          handoff.release()
          setError(friendlyAuthError(err, 'login'))
        },
      },
    )
  }

  const clearError = () => {
    if (error) setError(null)
  }

  return (
    <div>
      <div className="au-head">
        <p className="au-kicker">{forDesktop ? 'nexus desktop · step 1 of 2' : 'welcome back'}</p>
        <h1 className="au-title">{forDesktop ? 'Log in to connect Nexus Desktop' : 'Log in to Nexus'}</h1>
        <p className="au-sub">
          {forDesktop
            ? 'Then you’ll approve the desktop app on this device.'
            : 'Pick up where your agents left off.'}
        </p>
      </div>

      {demo ? (
        <div className="au-demo-note">
          <Sparkles size={16} aria-hidden />
          <span>
            This is the demo. Enter any name and password to go back to the sample workspace.
            Nothing is sent to a server.
          </span>
        </div>
      ) : null}

      <FormAlert error={error}>
        {error?.action === 'forgot' ? (
          <p style={{ marginTop: 6 }}>
            <Link to={`/forgot-password${keep}`}>Reset your password</Link>
          </p>
        ) : null}
      </FormAlert>

      <form className="au-form" onSubmit={onSubmit} noValidate>
        <Field
          label="Email or username"
          name="username"
          autoComplete="username"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          enterKeyHint="next"
          value={identifier}
          onValue={(v) => {
            setIdentifier(v)
            clearError()
          }}
          validate={(v) => (v.trim() ? null : 'Enter your email or username.')}
          showErrors={submitted}
          autoFocus
        />
        <PasswordField
          label="Password"
          name="password"
          autoComplete="current-password"
          enterKeyHint="go"
          value={password}
          onValue={(v) => {
            setPassword(v)
            clearError()
          }}
          validate={(v) => (v ? null : 'Enter your password.')}
          showErrors={submitted}
          aside={
            <Link className="au-label-aside" to={`/forgot-password${keep}`}>
              Forgot password?
            </Link>
          }
        />
        <SubmitButton busy={login.isPending} done={done} busyLabel="Logging in…" doneLabel="Signed in">
          Log in
        </SubmitButton>
      </form>

      <p className="au-alt">
        New to Nexus? <Link to={`/signup${keep}`}>Create a free account</Link>
      </p>
    </div>
  )
}

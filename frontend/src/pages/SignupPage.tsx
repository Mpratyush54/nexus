import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { useToast } from '@/components/ui/Toast'
import {
  useCancelSignup,
  useResendSignupCode,
  useSendSignupCode,
  useSignup,
  useSignupStatus,
} from '@/hooks/useAuthMutations'
import { ApiError } from '@/types/api'
import { storage } from '@/utils/storage'

function looksLikeEmail(value: string) {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)
}

type Step = 'details' | 'resume' | 'verify'

export function SignupPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const { push } = useToast()
  const sendCode = useSendSignupCode()
  const resendCode = useResendSignupCode()
  const signup = useSignup()
  const cancelSignup = useCancelSignup()
  const statusCheck = useSignupStatus()
  const draft = storage.getSignupDraft()
  const [step, setStep] = useState<Step>(draft?.step === 'verify' ? 'verify' : 'details')
  const [email, setEmail] = useState(draft?.email ?? '')
  const [username, setUsername] = useState(draft?.username ?? '')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [resumeNote, setResumeNote] = useState(
    draft?.step === 'verify' ? `You left verification unfinished for ${draft.email}.` : '',
  )
  const next = params.get('next')

  useEffect(() => {
    if (!draft?.email || draft.step !== 'verify') return
    statusCheck.mutate(draft.email, {
      onSuccess: (res) => {
        if (!res.pending) {
          setResumeNote('')
          setStep('details')
          storage.clearSignupDraft()
          return
        }
        if (res.username) setUsername(res.username)
        if (res.expired) {
          setResumeNote('Your previous code expired. Tap Resend code — same password, any device.')
        } else {
          setResumeNote(
            `Continue where you left off — code still valid for about ${Math.max(1, Math.round((res.expires_in ?? 60) / 60))} min.`,
          )
        }
        setStep('verify')
      },
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps -- run once on mount for draft resume
  }, [])

  const persist = (nextStep: 'details' | 'verify', mail = email, user = username) => {
    if (looksLikeEmail(mail.trim()) && user.trim().length >= 2) {
      storage.setSignupDraft({ email: mail.trim(), username: user.trim(), step: nextStep })
    } else if (looksLikeEmail(mail.trim())) {
      storage.setSignupDraft({
        email: mail.trim(),
        username: user.trim() || 'pending',
        step: nextStep,
      })
    }
  }

  const afterSuccess = (user: string) => {
    storage.clearSignupDraft()
    push({ title: 'Account created', detail: `Welcome, ${user}` })
    if (next && (next.startsWith('/cli/') || next.startsWith('/app/'))) {
      navigate(next, { replace: true })
      return
    }
    navigate('/app/dashboard')
  }

  const startOver = () => {
    const mail = email.trim()
    const clearLocal = () => {
      storage.clearSignupDraft()
      setStep('details')
      setCode('')
      setPassword('')
      setResumeNote('')
      push({ title: 'Started over', detail: 'Pending verification cleared. You can sign up again.' })
    }
    if (looksLikeEmail(mail)) {
      cancelSignup.mutate(mail, { onSettled: clearLocal })
    } else {
      clearLocal()
    }
  }

  const enterVerify = (mail: string, user: string, note: string, maybeDev?: string) => {
    setEmail(mail)
    if (user) setUsername(user)
    setStep('verify')
    persist('verify', mail, user || username)
    setResumeNote(note)
    if (maybeDev) {
      setCode(maybeDev)
      push({ title: 'Dev code ready', detail: maybeDev })
    }
  }

  const onSendCode = (e: FormEvent) => {
    e.preventDefault()
    const mail = email.trim()
    const user = username.trim()
    if (!looksLikeEmail(mail)) {
      push({ title: 'Email required', detail: 'Enter a valid work email.', tone: 'danger' })
      return
    }
    if (user.length < 2) {
      push({ title: 'Username required', detail: 'Pick a username (2+ characters).', tone: 'danger' })
      return
    }
    if (password.length < 8) {
      push({ title: 'Password too short', detail: 'Use at least 8 characters.', tone: 'danger' })
      return
    }
    sendCode.mutate(
      { username: user, password, email: mail },
      {
        onSuccess: (res) => {
          enterVerify(
            mail,
            user,
            '',
            res.dev_code,
          )
          if (!res.dev_code) {
            push({ title: 'Check your email', detail: `We sent a 6-digit code to ${res.email}` })
          }
        },
        onError: (err) => {
          const message = err instanceof ApiError ? err.message : 'Could not send code'
          push({ title: 'Could not send code', detail: message, tone: 'danger' })
        },
      },
    )
  }

  const onResumeLookup = (e: FormEvent) => {
    e.preventDefault()
    const mail = email.trim()
    if (!looksLikeEmail(mail)) {
      push({ title: 'Email required', detail: 'Enter the email you started with.', tone: 'danger' })
      return
    }
    statusCheck.mutate(mail, {
      onSuccess: (res) => {
        if (!res.pending) {
          push({
            title: 'No pending signup',
            detail: 'No open verification for this email. Start a new signup on this device.',
            tone: 'danger',
          })
          return
        }
        const user = res.username || ''
        if (res.expired) {
          enterVerify(
            mail,
            user,
            `Your earlier code expired. Tap Resend code below — password stays the same.`,
          )
          onResendAfterEnter(mail)
          return
        }
        enterVerify(
          mail,
          user,
          `Continuing on this device as ${user || 'you'}. Enter the code from your email (or resend below).`,
        )
        push({
          title: 'Pending signup found',
          detail: `Code valid ~${Math.max(1, Math.round((res.expires_in ?? 60) / 60))} more min.`,
        })
      },
      onError: (err) => {
        const message = err instanceof ApiError ? err.message : 'Could not look up signup'
        push({ title: 'Lookup failed', detail: message, tone: 'danger' })
      },
    })
  }

  const onResendAfterEnter = (mail: string) => {
    resendCode.mutate(mail, {
      onSuccess: (res) => {
        if (res.username) setUsername(res.username)
        if (res.dev_code) {
          setCode(res.dev_code)
          push({ title: 'New code sent', detail: res.dev_code })
        } else {
          push({ title: 'New code sent', detail: `Check ${res.email}` })
        }
      },
      onError: (err) => {
        const message = err instanceof ApiError ? err.message : 'Could not resend'
        push({ title: 'Resend failed', detail: message, tone: 'danger' })
      },
    })
  }

  const onVerify = (e: FormEvent) => {
    e.preventDefault()
    const otp = code.trim()
    if (otp.length < 4) {
      push({ title: 'Code required', detail: 'Enter the verification code from your email.', tone: 'danger' })
      return
    }
    signup.mutate(
      {
        email: email.trim(),
        code: otp,
        username: username.trim() || undefined,
      },
      {
        onSuccess: (res) => afterSuccess(res.username),
        onError: (err) => {
          const message = err instanceof ApiError ? err.message : 'Signup failed'
          push({ title: 'Could not verify', detail: message, tone: 'danger' })
        },
      },
    )
  }

  const onResend = () => {
    const mail = email.trim()
    if (!looksLikeEmail(mail)) return
    resendCode.mutate(mail, {
      onSuccess: (res) => {
        if (res.username) setUsername(res.username)
        persist('verify', mail, res.username || username)
        if (res.dev_code) {
          setCode(res.dev_code)
          push({ title: 'New dev code', detail: res.dev_code })
        } else {
          push({ title: 'Code resent', detail: `Check ${res.email}` })
        }
      },
      onError: (err) => {
        const message = err instanceof ApiError ? err.message : 'Could not resend'
        push({ title: 'Resend failed', detail: message, tone: 'danger' })
      },
    })
  }

  const stepCopy =
    step === 'details'
      ? 'Free plan · we verify your email before creating the account.'
      : step === 'resume'
        ? 'Started signup on another phone or laptop? Enter that email to finish here.'
        : `Enter the code we sent to ${email.trim() || 'your email'}. Works on any device — password was saved when you first requested the code.`

  return (
    <div className="mesh-bg flex min-h-svh items-center justify-center px-4 py-12">
      <div className="w-full max-w-md">
        <Link to="/" className="mb-8 block text-center font-[family-name:var(--font-display)] text-2xl font-semibold tracking-tight text-fg">
          Nexus
        </Link>
        <GlassPanel className="p-6 sm:p-7">
          <h1 className="text-xl font-semibold text-fg">
            {step === 'resume' ? 'Continue on this device' : 'Create your account'}
          </h1>
          <p className="mt-1 text-sm text-fg-dim">{stepCopy}</p>

          {resumeNote && step === 'verify' ? (
            <div className="mt-4 rounded-lg border border-amber/30 bg-amber/10 px-3 py-2 text-xs text-fg">
              <p>{resumeNote}</p>
              <div className="mt-2 flex flex-wrap gap-3">
                <button
                  type="button"
                  className="text-fg-dim hover:text-fg hover:underline"
                  disabled={cancelSignup.isPending}
                  onClick={startOver}
                >
                  Start over
                </button>
              </div>
            </div>
          ) : null}

          {step === 'details' ? (
            <form onSubmit={onSendCode} className="mt-6 space-y-4">
              <label className="block">
                <span className="mb-1.5 block text-xs text-fg-dim">Work email</span>
                <input
                  required
                  type="email"
                  autoComplete="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
                  placeholder="you@company.com"
                />
              </label>
              <label className="block">
                <span className="mb-1.5 block text-xs text-fg-dim">Username</span>
                <input
                  required
                  minLength={2}
                  autoComplete="username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
                  placeholder="pratyush"
                />
              </label>
              <label className="block">
                <span className="mb-1.5 block text-xs text-fg-dim">Password</span>
                <input
                  required
                  type="password"
                  minLength={8}
                  autoComplete="new-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
                  placeholder="At least 8 characters"
                />
              </label>
              <Button type="submit" className="w-full" size="lg" disabled={sendCode.isPending}>
                {sendCode.isPending ? 'Sending code…' : 'Send verification code'}
              </Button>
              <button
                type="button"
                className="w-full text-center text-sm text-ember hover:underline"
                onClick={() => {
                  setStep('resume')
                  setResumeNote('')
                  setCode('')
                }}
              >
                Already started on another device?
              </button>
            </form>
          ) : null}

          {step === 'resume' ? (
            <form onSubmit={onResumeLookup} className="mt-6 space-y-4">
              <label className="block">
                <span className="mb-1.5 block text-xs text-fg-dim">Email you used to sign up</span>
                <input
                  required
                  type="email"
                  autoComplete="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
                  placeholder="you@company.com"
                />
              </label>
              <Button type="submit" className="w-full" size="lg" disabled={statusCheck.isPending}>
                {statusCheck.isPending ? 'Looking up…' : 'Find my pending signup'}
              </Button>
              <button
                type="button"
                className="w-full text-center text-sm text-fg-dim hover:text-fg hover:underline"
                onClick={() => setStep('details')}
              >
                Back to new signup
              </button>
            </form>
          ) : null}

          {step === 'verify' ? (
            <form onSubmit={onVerify} className="mt-6 space-y-4">
              <label className="block">
                <span className="mb-1.5 block text-xs text-fg-dim">Verification code</span>
                <input
                  required
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 8))}
                  className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-center font-mono text-lg tracking-[0.3em] text-fg outline-none transition focus:border-amber"
                  placeholder="000000"
                />
              </label>
              <Button type="submit" className="w-full" size="lg" disabled={signup.isPending}>
                {signup.isPending ? 'Creating…' : 'Verify & create account'}
              </Button>
              <div className="flex flex-wrap justify-between gap-2 text-sm">
                <button
                  type="button"
                  className="text-fg-dim hover:text-fg hover:underline"
                  onClick={() => {
                    setStep('details')
                    setCode('')
                    persist('details')
                    setResumeNote('Change details below, then send a new code.')
                  }}
                >
                  Edit details
                </button>
                <button
                  type="button"
                  className="text-ember hover:underline disabled:opacity-50"
                  disabled={resendCode.isPending}
                  onClick={onResend}
                >
                  {resendCode.isPending ? 'Resending…' : 'Resend code'}
                </button>
              </div>
              <button
                type="button"
                className="w-full text-center text-xs text-muted hover:text-fg hover:underline"
                disabled={cancelSignup.isPending}
                onClick={startOver}
              >
                Abort signup / start over
              </button>
            </form>
          ) : null}

          <p className="mt-5 text-center text-sm text-fg-dim">
            Already have an account?{' '}
            <Link
              to={next ? `/login?next=${encodeURIComponent(next)}` : '/login'}
              className="text-ember hover:underline"
            >
              Log in
            </Link>
          </p>
        </GlassPanel>
      </div>
    </div>
  )
}

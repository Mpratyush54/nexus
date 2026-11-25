import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { useToast } from '@/components/ui/Toast'
import {
  useCancelSignup,
  useResendSignupCode,
  useSendSignupCode,
  useSignup,
  useSignupStatus,
} from '@/hooks/useAuthMutations'
import { friendlyAuthError, SUPPORT_EMAIL, type FriendlyError } from '@/auth/errors'
import { authApi, type MailDelivery } from '@/api/auth'
import { Field, FormAlert, PasswordField, SubmitButton } from '@/auth/fields'
import { looksLikeEmail } from '@/auth/validation'
import { afterAuthPath, handoff, prefetchApp, useReleaseHoldOnLeave } from '@/auth/handoff'
import { StepSwap } from '@/auth/StepSwap'
import { storage } from '@/utils/storage'

type Step = 'details' | 'resume' | 'verify'
type Notice = { tone: 'info' | 'ok'; text: string } | null

const minutes = (secs?: number) => Math.max(1, Math.round((secs ?? 60) / 60))

/** Seconds before "Send a new code" works again after a code was sent. */
const RESEND_COOLDOWN_SECS = 30
/** How long to keep asking the server whether the code email went out. */
const DELIVERY_WATCH_MS = 60_000
const DELIVERY_POLL_MS = 3_000

/** The message for a code email the server gave up on. */
const mailFailedError: FriendlyError = {
  title: 'We couldn’t send the code email',
  detail: `Our email service is having trouble. Send a new code; if it fails again, contact ${SUPPORT_EMAIL}.`,
  action: 'resend',
}

export function SignupPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const { push } = useToast()
  const sendCode = useSendSignupCode()
  const resendCode = useResendSignupCode()
  const signup = useSignup()
  const cancelSignup = useCancelSignup()
  const statusCheck = useSignupStatus()
  const [draft] = useState(() => storage.getSignupDraft())
  const [step, setStep] = useState<Step>(draft?.step === 'verify' ? 'verify' : 'details')
  const [email, setEmail] = useState(draft?.email ?? '')
  const [username, setUsername] = useState(draft?.username ?? '')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const [error, setError] = useState<FriendlyError | null>(null)
  const [notice, setNotice] = useState<Notice>(
    draft?.step === 'verify' ? { tone: 'info', text: `You left verification unfinished for ${draft.email}.` } : null,
  )
  const [done, setDone] = useState(false)
  // Resend cooldown and background-delivery watch for the last code email.
  const [cooldownUntil, setCooldownUntil] = useState(0)
  const [now, setNow] = useState(() => Date.now())
  const [watching, setWatching] = useState<{ email: string; since: number } | null>(null)
  const cooldown = Math.max(0, Math.ceil((cooldownUntil - now) / 1000))
  const next = params.get('next')
  const keep = next ? `?next=${encodeURIComponent(next)}` : ''
  useReleaseHoldOnLeave()

  useEffect(() => {
    if (!draft?.email || draft.step !== 'verify') return
    statusCheck.mutate(draft.email, {
      onSuccess: (res) => {
        if (!res.pending) {
          setNotice(null)
          setStep('details')
          storage.clearSignupDraft()
          return
        }
        if (res.username) setUsername(res.username)
        setNotice(
          res.expired
            ? { tone: 'info', text: 'Your previous code expired. Send a new one below; your password stays the same.' }
            : { tone: 'info', text: `Continue where you left off. Your code is valid for about ${minutes(res.expires_in)} more min.` },
        )
        setStep('verify')
      },
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps -- run once on mount for draft resume
  }, [])

  const go = (s: Step) => {
    setError(null)
    setSubmitted(false)
    setStep(s)
  }

  const persist = (nextStep: 'details' | 'verify', mail = email, user = username) => {
    if (looksLikeEmail(mail.trim())) {
      storage.setSignupDraft({ email: mail.trim(), username: user.trim(), step: nextStep })
    }
  }

  useEffect(() => {
    if (cooldownUntil <= Date.now()) return
    const t = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(t)
  }, [cooldownUntil])

  // The server emails the code in the background; ask how it went so a
  // failed send shows up here instead of as a code that never arrives.
  useEffect(() => {
    if (!watching || step !== 'verify') return
    let stopped = false
    const poll = async () => {
      if (stopped) return
      const res = await authApi.signupStatus(watching.email).catch(() => null)
      if (stopped) return
      const delivery: MailDelivery | undefined = res?.delivery
      if (delivery === 'failed') {
        setWatching(null)
        setCooldownUntil(0)
        setError(mailFailedError)
        return
      }
      if (delivery === 'sent' || !res?.pending || Date.now() - watching.since > DELIVERY_WATCH_MS) {
        setWatching(null)
        return
      }
      timer = window.setTimeout(poll, DELIVERY_POLL_MS)
    }
    let timer = window.setTimeout(poll, DELIVERY_POLL_MS)
    return () => {
      stopped = true
      window.clearTimeout(timer)
    }
  }, [watching, step])

  const codeSent = (mail: string, delivery?: MailDelivery) => {
    setCooldownUntil(Date.now() + RESEND_COOLDOWN_SECS * 1000)
    setNow(Date.now())
    setWatching(delivery === 'sending' ? { email: mail, since: Date.now() } : null)
  }

  const startOver = () => {
    const mail = email.trim()
    const clearLocal = () => {
      storage.clearSignupDraft()
      go('details')
      setCode('')
      setPassword('')
      setNotice({ tone: 'ok', text: 'Started over. Nothing was created; fill in your details again.' })
    }
    if (looksLikeEmail(mail)) cancelSignup.mutate(mail, { onSettled: clearLocal })
    else clearLocal()
  }

  const enterVerify = (mail: string, user: string, note: Notice, maybeDev?: string) => {
    setEmail(mail)
    if (user) setUsername(user)
    go('verify')
    persist('verify', mail, user || username)
    setNotice(note)
    if (maybeDev) {
      setCode(maybeDev)
      push({ title: 'Dev code filled in', detail: maybeDev })
    }
  }

  const emailProblem = (v: string) => (!v.trim() ? 'Enter your email.' : looksLikeEmail(v) ? null : 'That email looks incomplete, e.g. you@company.com.')
  const userProblem = (v: string) =>
    !v.trim() ? 'Choose a username. Teammates see it next to your chats.' : v.trim().length >= 2 ? null : 'Use at least 2 characters.'
  const passProblem = (v: string) => (v.length >= 8 ? null : `Use at least 8 characters${v ? ` (${8 - v.length} more)` : ''}.`)

  const onSendCode = (e: FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    const mail = email.trim()
    const user = username.trim()
    if (emailProblem(mail) || userProblem(user) || passProblem(password) || sendCode.isPending) return
    setError(null)
    sendCode.mutate(
      { username: user, password, email: mail },
      {
        onSuccess: (res) => {
          enterVerify(mail, user, null, res.dev_code)
          codeSent(mail, res.delivery)
        },
        onError: (err) => setError(friendlyAuthError(err, 'signup')),
      },
    )
  }

  const onResumeLookup = (e: FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    const mail = email.trim()
    if (emailProblem(mail) || statusCheck.isPending) return
    setError(null)
    statusCheck.mutate(mail, {
      onSuccess: (res) => {
        if (!res.pending) {
          setError({ title: 'No sign-up in progress for this email', detail: 'Start a new sign-up here instead.', action: 'restart' })
          return
        }
        const user = res.username || ''
        if (res.expired) {
          enterVerify(mail, user, { tone: 'info', text: 'Your earlier code expired, so we sent a new one. Your password stays the same.' })
          resend(mail)
          return
        }
        enterVerify(mail, user, {
          tone: 'info',
          text: `Continuing as ${user || 'you'}. Your code is valid for about ${minutes(res.expires_in)} more min.`,
        })
      },
      onError: (err) => setError(friendlyAuthError(err, 'resume')),
    })
  }

  const resend = (mail = email.trim()) => {
    if (!looksLikeEmail(mail) || resendCode.isPending) return
    setError(null)
    resendCode.mutate(mail, {
      onSuccess: (res) => {
        if (res.username) setUsername(res.username)
        persist('verify', mail, res.username || username)
        if (res.dev_code) {
          setCode(res.dev_code)
          push({ title: 'New dev code filled in', detail: res.dev_code })
        }
        setNotice({ tone: 'ok', text: `New code sent to ${res.email}.` })
        codeSent(mail, res.delivery)
      },
      onError: (err) => setError(friendlyAuthError(err, 'resend')),
    })
  }

  const onVerify = (e: FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    const otp = code.trim()
    if (otp.length < 4 || signup.isPending || done) return
    setError(null)
    handoff.hold()
    prefetchApp()
    signup.mutate(
      { email: email.trim(), code: otp, username: username.trim() || undefined },
      {
        onSuccess: () => {
          storage.clearSignupDraft()
          setDone(true)
          handoff.cover()
          window.setTimeout(() => navigate(afterAuthPath(next), { replace: true }), 240)
        },
        onError: (err) => {
          handoff.release()
          setError(friendlyAuthError(err, 'verify'))
        },
      },
    )
  }

  const errorAction =
    error?.action === 'login' ? (
      <p style={{ marginTop: 6 }}>
        <Link to={`/login${keep}`}>Log in instead</Link>
      </p>
    ) : error?.action === 'resend' && step === 'verify' ? (
      <p style={{ marginTop: 6 }}>
        <button type="button" onClick={() => resend()} disabled={resendCode.isPending || cooldown > 0}>
          {cooldown > 0 ? `Send a new code (${cooldown}s)` : 'Send a new code'}
        </button>
      </p>
    ) : error?.action === 'restart' ? (
      <p style={{ marginTop: 6 }}>
        <button type="button" onClick={startOver}>
          Start over
        </button>
      </p>
    ) : null

  return (
    <div>
      <StepSwap step={step}>
        <div className="au-head">
          <p className="au-kicker">
            {step === 'verify' ? 'step 2 of 2 · verify email' : step === 'resume' ? 'continue sign-up' : 'free plan · step 1 of 2'}
          </p>
          <h1 className="au-title">
            {step === 'verify' ? 'Check your email' : step === 'resume' ? 'Finish signing up here' : 'Create your Nexus account'}
          </h1>
          <p className="au-sub">
            {step === 'verify' ? (
              <>
                We sent a 6-digit code to <strong>{email.trim() || 'your email'}</strong>. It works on any
                device.
              </>
            ) : step === 'resume' ? (
              'Started on another phone or laptop? Enter the same email to pick it up here.'
            ) : (
              'Your agents’ memory, synced across your machines and your team.'
            )}
          </p>
        </div>
      </StepSwap>

      <FormAlert error={error}>{errorAction}</FormAlert>
      {!error && notice ? <FormAlert tone={notice.tone}>{notice.text}</FormAlert> : null}

      <StepSwap step={step}>
        {step === 'details' ? (
          <form className="au-form" onSubmit={onSendCode} noValidate>
            <Field
              label="Email"
              name="email"
              type="email"
              autoComplete="email"
              inputMode="email"
              autoCapitalize="none"
              spellCheck={false}
              enterKeyHint="next"
              placeholder="you@company.com"
              value={email}
              onValue={setEmail}
              validate={emailProblem}
              showErrors={submitted}
              autoFocus
            />
            <Field
              label="Username"
              name="username"
              autoComplete="username"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              enterKeyHint="next"
              value={username}
              onValue={setUsername}
              validate={userProblem}
              showErrors={submitted}
              hint="Shown to teammates. You can log in with it or your email."
            />
            <PasswordField
              label="Password"
              name="new-password"
              autoComplete="new-password"
              enterKeyHint="go"
              value={password}
              onValue={setPassword}
              validate={passProblem}
              showErrors={submitted}
              strength={{ others: [username.trim(), email.split('@')[0] ?? ''] }}
              hint="At least 8 characters. A few random words work well."
            />
            <SubmitButton busy={sendCode.isPending} busyLabel="Sending code…">
              Continue
            </SubmitButton>
            <p className="au-note au-legal">
              By continuing you agree to the <a href="/terms">Terms</a> and <a href="/privacy">Privacy Policy</a>.
            </p>
            <div className="au-row">
              <button type="button" className="au-link is-quiet" onClick={() => go('resume')}>
                Already got a code by email? Finish signing up
              </button>
            </div>
          </form>
        ) : step === 'resume' ? (
          <form className="au-form" onSubmit={onResumeLookup} noValidate>
            <Field
              label="Email you signed up with"
              name="email"
              type="email"
              autoComplete="email"
              inputMode="email"
              autoCapitalize="none"
              spellCheck={false}
              value={email}
              onValue={setEmail}
              validate={emailProblem}
              showErrors={submitted}
              autoFocus
            />
            <SubmitButton busy={statusCheck.isPending} busyLabel="Looking it up…">
              Find my sign-up
            </SubmitButton>
            <div className="au-row">
              <button type="button" className="au-link is-quiet" onClick={() => go('details')}>
                Back to a new sign-up
              </button>
            </div>
          </form>
        ) : (
          <form className="au-form" onSubmit={onVerify} noValidate>
            {/* Lets password managers pair the saved password with this account. */}
            <input type="text" name="username" autoComplete="username" value={email} readOnly hidden />
            <Field
              label="Verification code"
              name="one-time-code"
              autoComplete="one-time-code"
              inputMode="numeric"
              enterKeyHint="go"
              code
              maxLength={8}
              placeholder="000000"
              value={code}
              onValue={(v) => setCode(v.replace(/\D/g, '').slice(0, 8))}
              validate={(v) => (v.trim().length >= 4 ? null : 'Enter the code from the email.')}
              showErrors={submitted}
              hint="Can’t find it? Check spam, or send a new code."
              autoFocus
            />
            <SubmitButton busy={signup.isPending} done={done} busyLabel="Creating your account…" doneLabel="Account created">
              Verify and create account
            </SubmitButton>
            <div className="au-row">
              <button
                type="button"
                className="au-link is-quiet"
                onClick={() => {
                  go('details')
                  setCode('')
                  persist('details')
                  setNotice({ tone: 'info', text: 'Change your details, then we’ll send a new code.' })
                }}
              >
                Edit details
              </button>
              <button type="button" className="au-link" disabled={resendCode.isPending || cooldown > 0} onClick={() => resend()}>
                {resendCode.isPending ? 'Sending…' : cooldown > 0 ? `Send a new code (${cooldown}s)` : 'Send a new code'}
              </button>
            </div>
            <p className="au-note">
              Changed your mind?{' '}
              <button type="button" className="au-link is-quiet" style={{ fontSize: 13 }} disabled={cancelSignup.isPending} onClick={startOver}>
                Cancel this sign-up
              </button>
            </p>
          </form>
        )}
      </StepSwap>

      <p className="au-alt">
        Already have an account? <Link to={`/login${keep}`}>Log in</Link>
      </p>
      {step === 'details' ? (
        <p className="au-note">Free plan. No card needed. We verify your email before creating the account.</p>
      ) : null}
    </div>
  )
}

import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { useToast } from '@/components/ui/Toast'
import { useForgotPassword, useResetPassword } from '@/hooks/useAuthMutations'
import { friendlyAuthError, type FriendlyError } from '@/auth/errors'
import { Field, FormAlert, PasswordField, SubmitButton } from '@/auth/fields'
import { looksLikeEmail } from '@/auth/validation'
import { StepSwap } from '@/auth/StepSwap'

type Notice = { tone: 'info' | 'ok'; text: string } | null

/**
 * Password reset by emailed code: POST /auth/password/forgot sends it (the
 * answer is the same whether or not the address has an account), then
 * POST /auth/password/reset sets the new password.
 */
export function ForgotPasswordPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const { push } = useToast()
  const forgot = useForgotPassword()
  const reset = useResetPassword()
  const [step, setStep] = useState<'request' | 'reset'>('request')
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const [error, setError] = useState<FriendlyError | null>(null)
  const [notice, setNotice] = useState<Notice>(null)
  const [done, setDone] = useState(false)
  const next = params.get('next')
  const keep = next ? `?next=${encodeURIComponent(next)}` : ''
  const loginHref = `/login${keep}`

  const emailProblem = (v: string) => (!v.trim() ? 'Enter your email.' : looksLikeEmail(v) ? null : 'That email looks incomplete, e.g. you@company.com.')
  const passProblem = (v: string) => (v.length >= 8 ? null : `Use at least 8 characters${v ? ` (${8 - v.length} more)` : ''}.`)
  const confirmProblem = (v: string) => (v && v === password ? null : v ? 'Doesn’t match the new password.' : 'Type the new password again.')

  const send = (onSent?: () => void) => {
    setError(null)
    forgot.mutate(email.trim(), {
      onSuccess: (res) => {
        if (res.dev_code) {
          setCode(res.dev_code)
          push({ title: 'Dev code filled in', detail: res.dev_code })
        }
        onSent?.()
      },
      onError: (err) => setError(friendlyAuthError(err, 'forgot')),
    })
  }

  const onRequest = (e: FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (emailProblem(email) || forgot.isPending) return
    send(() => {
      setSubmitted(false)
      setNotice(null)
      setStep('reset')
    })
  }

  const onReset = (e: FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (code.trim().length < 4 || passProblem(password) || confirmProblem(confirm) || reset.isPending || done) return
    setError(null)
    reset.mutate(
      { email: email.trim(), code: code.trim(), new_password: password },
      {
        onSuccess: () => {
          setDone(true)
          push({ title: 'Password updated', detail: 'Log in with your new password.' })
          window.setTimeout(() => navigate(loginHref, { replace: true }), 700)
        },
        onError: (err) => setError(friendlyAuthError(err, 'reset')),
      },
    )
  }

  return (
    <div>
      <StepSwap step={step}>
        <div className="au-head">
          <p className="au-kicker">{step === 'request' ? 'reset password · step 1 of 2' : 'reset password · step 2 of 2'}</p>
          <h1 className="au-title">{step === 'request' ? 'Forgot your password?' : 'Choose a new password'}</h1>
          <p className="au-sub">
            {step === 'request' ? (
              'Enter your account email and we’ll send a 6-digit code to reset it.'
            ) : (
              <>
                If <strong>{email.trim()}</strong> has a Nexus account, a code is on its way. Enter it with
                your new password.
              </>
            )}
          </p>
        </div>
      </StepSwap>

      <FormAlert error={error}>
        {error?.action === 'resend' ? (
          <p style={{ marginTop: 6 }}>
            <button type="button" onClick={() => send(() => setNotice({ tone: 'ok', text: 'New code sent.' }))}>
              Send a new code
            </button>
          </p>
        ) : null}
      </FormAlert>
      {!error && notice ? <FormAlert tone={notice.tone}>{notice.text}</FormAlert> : null}

      <StepSwap step={step}>
        {step === 'request' ? (
          <form className="au-form" onSubmit={onRequest} noValidate>
            <Field
              label="Account email"
              name="email"
              type="email"
              autoComplete="email"
              inputMode="email"
              autoCapitalize="none"
              spellCheck={false}
              enterKeyHint="send"
              placeholder="you@company.com"
              value={email}
              onValue={(v) => {
                setEmail(v)
                if (error) setError(null)
              }}
              validate={emailProblem}
              showErrors={submitted}
              autoFocus
            />
            <SubmitButton busy={forgot.isPending} busyLabel="Sending code…">
              Send reset code
            </SubmitButton>
          </form>
        ) : (
          <form className="au-form" onSubmit={onReset} noValidate>
            <input type="text" name="username" autoComplete="username" value={email} readOnly hidden />
            <Field
              label="Reset code"
              name="one-time-code"
              autoComplete="one-time-code"
              inputMode="numeric"
              enterKeyHint="next"
              code
              maxLength={8}
              placeholder="000000"
              value={code}
              onValue={(v) => setCode(v.replace(/\D/g, '').slice(0, 8))}
              validate={(v) => (v.trim().length >= 4 ? null : 'Enter the code from the email.')}
              showErrors={submitted}
              autoFocus
            />
            <PasswordField
              label="New password"
              name="new-password"
              autoComplete="new-password"
              enterKeyHint="next"
              value={password}
              onValue={setPassword}
              validate={passProblem}
              showErrors={submitted}
              strength={{ others: [email.split('@')[0] ?? ''] }}
            />
            <PasswordField
              label="Confirm new password"
              name="confirm-password"
              autoComplete="new-password"
              enterKeyHint="go"
              value={confirm}
              onValue={setConfirm}
              validate={confirmProblem}
              showErrors={submitted}
            />
            <SubmitButton busy={reset.isPending} done={done} busyLabel="Updating…" doneLabel="Password updated">
              Update password
            </SubmitButton>
            <div className="au-row">
              <button
                type="button"
                className="au-link is-quiet"
                onClick={() => {
                  setStep('request')
                  setSubmitted(false)
                  setError(null)
                  setNotice(null)
                  setCode('')
                  setPassword('')
                  setConfirm('')
                }}
              >
                Use a different email
              </button>
              <button
                type="button"
                className="au-link"
                disabled={forgot.isPending}
                onClick={() => send(() => setNotice({ tone: 'ok', text: `New code sent to ${email.trim()}.` }))}
              >
                {forgot.isPending ? 'Sending…' : 'Send a new code'}
              </button>
            </div>
          </form>
        )}
      </StepSwap>

      <p className="au-alt">
        Remembered it? <Link to={loginHref}>Log in</Link>
      </p>
    </div>
  )
}

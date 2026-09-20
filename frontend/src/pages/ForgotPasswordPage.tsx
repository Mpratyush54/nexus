import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { useToast } from '@/components/ui/Toast'
import { useForgotPassword, useResetPassword } from '@/hooks/useAuthMutations'
import { ApiError } from '@/types/api'

function looksLikeEmail(value: string) {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)
}

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
  const next = params.get('next')

  const loginHref = next ? `/login?next=${encodeURIComponent(next)}` : '/login'

  const onRequest = (e: FormEvent) => {
    e.preventDefault()
    const mail = email.trim()
    if (!looksLikeEmail(mail)) {
      push({ title: 'Email required', detail: 'Enter the email on your account.', tone: 'danger' })
      return
    }
    forgot.mutate(mail, {
      onSuccess: (res) => {
        setStep('reset')
        if (res.dev_code) {
          setCode(res.dev_code)
          push({ title: 'Dev code ready', detail: res.dev_code })
        } else {
          push({
            title: 'Check your email',
            detail: 'If that address has an account, we sent a reset code.',
          })
        }
      },
      onError: (err) => {
        const message = err instanceof ApiError ? err.message : 'Could not send reset email'
        push({ title: 'Request failed', detail: message, tone: 'danger' })
      },
    })
  }

  const onReset = (e: FormEvent) => {
    e.preventDefault()
    const otp = code.trim()
    if (otp.length < 4) {
      push({ title: 'Code required', detail: 'Enter the code from your email.', tone: 'danger' })
      return
    }
    if (password.length < 8) {
      push({ title: 'Password too short', detail: 'Use at least 8 characters.', tone: 'danger' })
      return
    }
    if (password !== confirm) {
      push({ title: 'Passwords differ', detail: 'New password and confirmation must match.', tone: 'danger' })
      return
    }
    reset.mutate(
      { email: email.trim(), code: otp, new_password: password },
      {
        onSuccess: () => {
          push({ title: 'Password updated', detail: 'Log in with your new password.' })
          navigate(loginHref, { replace: true })
        },
        onError: (err) => {
          const message = err instanceof ApiError ? err.message : 'Could not reset password'
          push({ title: 'Reset failed', detail: message, tone: 'danger' })
        },
      },
    )
  }

  return (
    <div className="mesh-bg flex min-h-svh items-center justify-center px-4 py-12">
      <div className="w-full max-w-md">
        <Link
          to="/"
          className="mb-8 block text-center font-[family-name:var(--font-display)] text-2xl font-semibold tracking-tight text-fg"
        >
          Nexus
        </Link>
        <GlassPanel className="p-6 sm:p-7">
          <h1 className="text-xl font-semibold text-fg">Forgot password</h1>
          <p className="mt-1 text-sm text-fg-dim">
            {step === 'request'
              ? 'We will email a one-time code so you can set a new password.'
              : `Enter the code sent to ${email.trim() || 'your email'} and choose a new password.`}
          </p>

          {step === 'request' ? (
            <form onSubmit={onRequest} className="mt-6 space-y-4">
              <label className="block">
                <span className="mb-1.5 block text-xs text-fg-dim">Account email</span>
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
              <Button type="submit" className="w-full" size="lg" disabled={forgot.isPending}>
                {forgot.isPending ? 'Sending…' : 'Send reset code'}
              </Button>
            </form>
          ) : (
            <form onSubmit={onReset} className="mt-6 space-y-4">
              <label className="block">
                <span className="mb-1.5 block text-xs text-fg-dim">Reset code</span>
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
              <label className="block">
                <span className="mb-1.5 block text-xs text-fg-dim">New password</span>
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
              <label className="block">
                <span className="mb-1.5 block text-xs text-fg-dim">Confirm password</span>
                <input
                  required
                  type="password"
                  minLength={8}
                  autoComplete="new-password"
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                  className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
                  placeholder="Repeat new password"
                />
              </label>
              <Button type="submit" className="w-full" size="lg" disabled={reset.isPending}>
                {reset.isPending ? 'Updating…' : 'Update password'}
              </Button>
              <div className="flex flex-wrap justify-between gap-2 text-sm">
                <button
                  type="button"
                  className="text-fg-dim hover:text-fg hover:underline"
                  onClick={() => {
                    setStep('request')
                    setCode('')
                    setPassword('')
                    setConfirm('')
                  }}
                >
                  Use a different email
                </button>
                <button
                  type="button"
                  className="text-ember hover:underline disabled:opacity-50"
                  disabled={forgot.isPending}
                  onClick={() =>
                    forgot.mutate(email.trim(), {
                      onSuccess: (res) => {
                        if (res.dev_code) {
                          setCode(res.dev_code)
                          push({ title: 'New dev code', detail: res.dev_code })
                        } else {
                          push({ title: 'Code resent', detail: 'Check your inbox' })
                        }
                      },
                      onError: (err) => {
                        const message = err instanceof ApiError ? err.message : 'Could not resend'
                        push({ title: 'Resend failed', detail: message, tone: 'danger' })
                      },
                    })
                  }
                >
                  {forgot.isPending ? 'Resending…' : 'Resend code'}
                </button>
              </div>
            </form>
          )}

          <p className="mt-5 text-center text-sm text-fg-dim">
            Remembered it?{' '}
            <Link to={loginHref} className="text-ember hover:underline">
              Log in
            </Link>
          </p>
        </GlassPanel>
      </div>
    </div>
  )
}

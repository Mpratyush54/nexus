import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { useToast } from '@/components/ui/Toast'
import { useSignup } from '@/hooks/useAuthMutations'
import { ApiError } from '@/types/api'

export function SignupPage() {
  const navigate = useNavigate()
  const { push } = useToast()
  const signup = useSignup()
  const [email, setEmail] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    signup.mutate(
      {
        username: username.trim(),
        password,
        email: email.trim() || undefined,
      },
      {
        onSuccess: () => {
          push({ title: 'Account created', detail: `Welcome, ${username.trim()}` })
          navigate('/app/dashboard')
        },
        onError: (err) => {
          const message = err instanceof ApiError ? err.message : 'Signup failed'
          push({ title: 'Could not sign up', detail: message, tone: 'danger' })
        },
      },
    )
  }

  return (
    <div className="mesh-bg flex min-h-svh items-center justify-center px-4 py-12">
      <div className="w-full max-w-md">
        <Link to="/" className="mb-8 block text-center text-lg font-semibold tracking-tight text-fg">
          Nexus
        </Link>
        <GlassPanel className="p-6 sm:p-7">
          <h1 className="text-xl font-semibold text-fg">Create account</h1>
          <p className="mt-1 text-sm text-fg-dim">Start a shared memory for your team.</p>
          <form onSubmit={onSubmit} className="mt-6 space-y-4">
            <label className="block">
              <span className="mb-1.5 block text-xs text-fg-dim">Username</span>
              <input
                required
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
                placeholder="pratyush"
              />
            </label>
            <label className="block">
              <span className="mb-1.5 block text-xs text-fg-dim">Email (optional)</span>
              <input
                type="email"
                autoComplete="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
                placeholder="you@team.dev"
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
            <Button type="submit" className="w-full" size="lg" disabled={signup.isPending}>
              {signup.isPending ? 'Creating…' : 'Create account'}
            </Button>
          </form>
          <p className="mt-5 text-center text-sm text-fg-dim">
            Already have an account?{' '}
            <Link to="/login" className="text-ember hover:underline">
              Log in
            </Link>
          </p>
        </GlassPanel>
      </div>
    </div>
  )
}

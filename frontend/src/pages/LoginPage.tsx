import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { useToast } from '@/components/ui/Toast'
import { useLogin } from '@/hooks/useAuthMutations'
import { ApiError } from '@/types/api'

export function LoginPage() {
  const navigate = useNavigate()
  const { push } = useToast()
  const login = useLogin()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    login.mutate(
      { username: username.trim(), password },
      {
        onSuccess: () => {
          push({ title: 'Signed in', detail: `Welcome, ${username.trim()}` })
          navigate('/app/memory')
        },
        onError: (err) => {
          const message = err instanceof ApiError ? err.message : 'Login failed'
          push({ title: 'Could not sign in', detail: message, tone: 'danger' })
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
          <h1 className="text-xl font-semibold text-fg">Log in</h1>
          <p className="mt-1 text-sm text-fg-dim">Continue to your project memory.</p>
          <form onSubmit={onSubmit} className="mt-6 space-y-4">
            <label className="block">
              <span className="mb-1.5 block text-xs text-fg-dim">Username</span>
              <input
                required
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
                placeholder="alice"
              />
            </label>
            <label className="block">
              <span className="mb-1.5 block text-xs text-fg-dim">Password</span>
              <input
                required
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition focus:border-amber"
                placeholder="••••••••"
              />
            </label>
            <Button type="submit" className="w-full" size="lg" disabled={login.isPending}>
              {login.isPending ? 'Signing in…' : 'Log in'}
            </Button>
          </form>
          <p className="mt-5 text-center text-sm text-fg-dim">
            No account yet?{' '}
            <Link to="/signup" className="text-ember hover:underline">
              Sign up
            </Link>
          </p>
        </GlassPanel>
      </div>
    </div>
  )
}

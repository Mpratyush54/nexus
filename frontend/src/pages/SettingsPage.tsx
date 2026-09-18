import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import {
  useCreateToken,
  useMe,
  useRevokeToken,
  useTokens,
  useUsage,
} from '@/hooks/useAuthMutations'
import { authApi } from '@/api/auth'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'

export function SettingsPage() {
  const { push } = useToast()
  const me = useMe()
  const usage = useUsage()
  const tokens = useTokens()
  const createToken = useCreateToken()
  const revokeToken = useRevokeToken()

  const [email, setEmail] = useState('')
  const [tokenName, setTokenName] = useState('')
  const [minted, setMinted] = useState<string | null>(null)
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')

  const onSaveEmail = (e: FormEvent) => {
    e.preventDefault()
    authApi
      .updateMe({ email: email.trim() })
      .then(() => {
        push({ title: 'Profile updated' })
        void me.refetch()
      })
      .catch((err) =>
        push({
          title: 'Update failed',
          detail: err instanceof ApiError ? err.message : 'Unknown error',
          tone: 'danger',
        }),
      )
  }

  const onChangePassword = (e: FormEvent) => {
    e.preventDefault()
    authApi
      .changePassword({
        current_password: currentPassword,
        new_password: newPassword,
      })
      .then(() => {
        push({ title: 'Password changed' })
        setCurrentPassword('')
        setNewPassword('')
      })
      .catch((err) =>
        push({
          title: 'Password change failed',
          detail: err instanceof ApiError ? err.message : 'Unknown error',
          tone: 'danger',
        }),
      )
  }

  const onMint = (e: FormEvent) => {
    e.preventDefault()
    createToken.mutate(tokenName.trim() || 'Agent token', {
      onSuccess: (res) => {
        setMinted(res.token)
        setTokenName('')
        push({ title: 'Token created', detail: 'Copy it now — it won’t be shown again.' })
      },
      onError: (err) =>
        push({
          title: 'Could not create token',
          detail: err instanceof ApiError ? err.message : 'Unknown error',
          tone: 'danger',
        }),
    })
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Settings</h1>
        <p className="mt-1 text-sm text-fg-dim">Profile, password, and API tokens.</p>
      </div>

      <GlassPanel className="space-y-3 p-5">
        <h2 className="text-sm font-medium text-fg">Profile</h2>
        {me.data ? (
          <>
            <p className="font-mono text-xs text-muted">@{me.data.username}</p>
            <form onSubmit={onSaveEmail} className="flex flex-wrap items-end gap-2">
              <label className="block min-w-[14rem] flex-1">
                <span className="mb-1 block text-xs text-fg-dim">Email</span>
                <input
                  type="email"
                  defaultValue={me.data.email ?? ''}
                  onChange={(e) => setEmail(e.target.value)}
                  className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
                />
              </label>
              <Button type="submit" size="sm">
                Save
              </Button>
            </form>
          </>
        ) : (
          <p className="text-sm text-muted">Loading profile…</p>
        )}
      </GlassPanel>

      {usage.data ? (
        <GlassPanel className="p-5">
          <h2 className="mb-3 text-sm font-medium text-fg">Usage</h2>
          <div className="flex flex-wrap gap-4 text-sm text-fg-dim">
            <span>{usage.data.memories_created} memories</span>
            <span>{usage.data.episodes_created} episodes</span>
            <span>~{usage.data.requests_approx} writes</span>
          </div>
        </GlassPanel>
      ) : null}

      <GlassPanel className="space-y-3 p-5">
        <h2 className="text-sm font-medium text-fg">Change password</h2>
        <form onSubmit={onChangePassword} className="grid max-w-md gap-3">
          <input
            type="password"
            required
            placeholder="Current password"
            value={currentPassword}
            onChange={(e) => setCurrentPassword(e.target.value)}
            className="h-10 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
          />
          <input
            type="password"
            required
            minLength={8}
            placeholder="New password"
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            className="h-10 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
          />
          <Button type="submit" size="sm" className="w-fit">
            Update password
          </Button>
        </form>
      </GlassPanel>

      <GlassPanel className="space-y-4 p-5">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-sm font-medium text-fg">API tokens</h2>
          <StatusPill tone="ember">revocable</StatusPill>
        </div>

        <form onSubmit={onMint} className="flex flex-wrap gap-2">
          <input
            value={tokenName}
            onChange={(e) => setTokenName(e.target.value)}
            placeholder="Token name (e.g. Cursor agent)"
            className="h-10 min-w-[14rem] flex-1 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
          />
          <Button type="submit" size="sm" disabled={createToken.isPending}>
            Create token
          </Button>
        </form>

        {minted ? (
          <div className="rounded-lg border border-amber/40 bg-amber-soft p-3">
            <p className="mb-1 text-xs text-amber">Copy now — shown once</p>
            <code className="break-all font-mono text-xs text-fg">{minted}</code>
          </div>
        ) : null}

        <ul className="space-y-2">
          {(tokens.data ?? []).map((t) => (
            <li
              key={t.id}
              className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border px-3 py-2"
            >
              <div>
                <p className="text-sm text-fg">{t.name}</p>
                <p className="font-mono text-[11px] text-muted">
                  {t.token_prefix}… · {formatRelative(t.created_at)}
                </p>
              </div>
              <Button
                variant="danger"
                size="sm"
                disabled={revokeToken.isPending}
                onClick={() =>
                  revokeToken.mutate(t.id, {
                    onSuccess: () => push({ title: 'Token revoked', detail: t.name }),
                  })
                }
              >
                Revoke
              </Button>
            </li>
          ))}
          {!tokens.isLoading && (tokens.data?.length ?? 0) === 0 ? (
            <li className="text-sm text-muted">No active tokens.</li>
          ) : null}
        </ul>
      </GlassPanel>
    </div>
  )
}

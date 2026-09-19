import { useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
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
import { notificationsApi } from '@/api/notifications'
import { projectsApi, type ExportMemory } from '@/api/projects'
import { PlanGrid } from '@/components/PlanGrid'
import { subscribeWebPush } from '@/lib/pwa'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'
import { useAuth } from '@/providers/AuthProvider'
import { useBillingPlans, useMyBilling } from '@/hooks/useBilling'

export function SettingsPage() {
  const { push } = useToast()
  const { projectId, setProjectId } = useAuth()
  const me = useMe()
  const usage = useUsage()
  const tokens = useTokens()
  const createToken = useCreateToken()
  const revokeToken = useRevokeToken()
  const plans = useBillingPlans()
  const billing = useMyBilling()

  const [email, setEmail] = useState('')
  const [tokenName, setTokenName] = useState('')
  const [minted, setMinted] = useState<string | null>(null)
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [pushBusy, setPushBusy] = useState(false)
  const [pushConfigured, setPushConfigured] = useState<boolean | null>(null)
  const [pushStatus, setPushStatus] = useState<string>(
    typeof Notification !== 'undefined' ? Notification.permission : 'unsupported',
  )
  const [exportBusy, setExportBusy] = useState(false)
  const [forkName, setForkName] = useState('')

  useEffect(() => {
    void notificationsApi
      .vapidPublicKey()
      .then((v) => setPushConfigured(Boolean(v.configured && v.public_key)))
      .catch(() => setPushConfigured(false))
  }, [])

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

  const onEnablePush = async () => {
    setPushBusy(true)
    try {
      const vapid = await notificationsApi.vapidPublicKey()
      setPushConfigured(Boolean(vapid.configured && vapid.public_key))
      if (!vapid.configured || !vapid.public_key) {
        push({
          title: 'Web Push not configured',
          detail: 'Set VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY on the server.',
          tone: 'amber',
        })
        return
      }
      const sub = await subscribeWebPush(vapid.public_key)
      if (!sub) {
        setPushStatus(Notification.permission)
        push({ title: 'Push not enabled', detail: 'Permission denied or unsupported', tone: 'danger' })
        return
      }
      const json = sub.toJSON()
      await notificationsApi.subscribe({
        endpoint: json.endpoint!,
        keys: {
          p256dh: json.keys?.p256dh ?? '',
          auth: json.keys?.auth ?? '',
        },
      })
      setPushStatus('granted')
      push({ title: 'Push notifications on', tone: 'teal' })
    } catch (err) {
      push({
        title: 'Push setup failed',
        detail: err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Unknown error',
        tone: 'danger',
      })
    } finally {
      setPushBusy(false)
    }
  }

  const onTestPush = async () => {
    setPushBusy(true)
    try {
      const res = await notificationsApi.testPush({
        title: 'Nexus test',
        body: 'Web Push delivery is working.',
        url: '/app/settings',
        project_id: projectId ?? undefined,
      })
      push({
        title: 'Test sent',
        detail: `${res.sent} delivered · ${res.failed} failed`,
        tone: res.sent > 0 ? 'teal' : 'amber',
      })
    } catch (err) {
      push({
        title: 'Test push failed',
        detail: err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Unknown error',
        tone: 'danger',
      })
    } finally {
      setPushBusy(false)
    }
  }

  const onExport = async (format: 'json' | 'yaml' | 'markdown') => {
    if (!projectId) return
    setExportBusy(true)
    try {
      await projectsApi.downloadExport(projectId, format)
      push({ title: 'Export started', detail: format, tone: 'teal' })
    } catch (err) {
      push({
        title: 'Export failed',
        detail: err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Unknown error',
        tone: 'danger',
      })
    } finally {
      setExportBusy(false)
    }
  }

  const onImportFile = async (file: File) => {
    if (!projectId) return
    setExportBusy(true)
    try {
      const text = await file.text()
      let result
      if (file.name.endsWith('.md') || file.type.includes('markdown')) {
        result = await projectsApi.importMarkdown(projectId, text)
      } else {
        const parsed = JSON.parse(text) as { memories?: ExportMemory[] } | ExportMemory[]
        const memories = Array.isArray(parsed) ? parsed : parsed.memories ?? []
        result = await projectsApi.importMemories(projectId, memories)
      }
      push({
        title: 'Import complete',
        detail: `${result.imported} imported · ${result.skipped} skipped`,
        tone: 'teal',
      })
    } catch (err) {
      push({
        title: 'Import failed',
        detail: err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Unknown error',
        tone: 'danger',
      })
    } finally {
      setExportBusy(false)
    }
  }

  const onFork = async () => {
    if (!projectId) return
    setExportBusy(true)
    try {
      const res = await projectsApi.fork(projectId, {
        folder_name: forkName.trim() || undefined,
      })
      setProjectId(res.project.id)
      setForkName('')
      push({
        title: 'Project forked',
        detail: `${res.memories_copied} memories copied`,
        tone: 'teal',
      })
    } catch (err) {
      push({
        title: 'Fork failed',
        detail: err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Unknown error',
        tone: 'danger',
      })
    } finally {
      setExportBusy(false)
    }
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Settings</h1>
        <p className="mt-1 text-sm text-fg-dim">
          Profile, password, and API tokens. PWA {import.meta.env.VITE_APP_VERSION ?? 'dev'}
          {me.data?.is_platform_admin ? (
            <>
              {' · '}
              <Link to="/app/admin" className="text-fg underline decoration-border underline-offset-2">
                Platform console
              </Link>
            </>
          ) : null}
        </p>
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

      <GlassPanel className="space-y-4 p-5">
        <div>
          <h2 className="text-sm font-medium text-fg">Plan</h2>
          <p className="mt-1 text-xs text-fg-dim">
            Personal subscription. You are on the free plan for now — paid upgrades open when
            checkout ships.
          </p>
        </div>
        {plans.data ? (
          <PlanGrid
            plans={plans.data}
            current={billing.data}
            canChange={false}
            allowPaidUpgrade={false}
          />
        ) : (
          <p className="text-sm text-muted">Loading plans…</p>
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

      <GlassPanel className="space-y-3 p-5">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="text-sm font-medium text-fg">Notifications</h2>
          <StatusPill tone={pushStatus === 'granted' ? 'teal' : 'neutral'}>{pushStatus}</StatusPill>
        </div>
        <p className="text-sm text-fg-dim">
          Enable browser push for handoffs, reviews, and mentions.{' '}
          <Link to="/app/notifications" className="text-fg underline decoration-border underline-offset-2">
            Open inbox, preferences, and devices
          </Link>
          .
          {pushConfigured === false
            ? ' Server is missing VAPID_PUBLIC_KEY / VAPID_PRIVATE_KEY.'
            : ' Delivery uses VAPID against your Push service subscription.'}
        </p>
        <div className="flex flex-wrap gap-2">
          <Button type="button" size="sm" disabled={pushBusy} onClick={() => void onEnablePush()}>
            {pushBusy ? 'Working…' : 'Enable web push'}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="secondary"
            disabled={pushBusy}
            onClick={() => void onTestPush()}
          >
            Send test
          </Button>
        </div>
      </GlassPanel>

      <GlassPanel className="space-y-3 p-5">
        <h2 className="text-sm font-medium text-fg">Export / import</h2>
        <p className="text-sm text-fg-dim">
          Download this project’s memories as JSON, YAML, or Markdown — or bulk-import a file. Fork
          clones memories and branch names into a new project.
        </p>
        <div className="flex flex-wrap gap-2">
          <Button type="button" size="sm" disabled={exportBusy || !projectId} onClick={() => void onExport('json')}>
            Export JSON
          </Button>
          <Button
            type="button"
            size="sm"
            variant="secondary"
            disabled={exportBusy || !projectId}
            onClick={() => void onExport('yaml')}
          >
            YAML
          </Button>
          <Button
            type="button"
            size="sm"
            variant="secondary"
            disabled={exportBusy || !projectId}
            onClick={() => void onExport('markdown')}
          >
            Markdown
          </Button>
          <label className="inline-flex h-8 cursor-pointer items-center rounded-lg border border-border bg-raised px-3 text-xs text-fg hover:border-border-strong">
            Import file
            <input
              type="file"
              accept=".json,.yaml,.yml,.md,application/json,text/markdown"
              className="hidden"
              onChange={(e) => {
                const file = e.target.files?.[0]
                if (file) void onImportFile(file)
                e.target.value = ''
              }}
            />
          </label>
        </div>
        <form
          className="flex flex-wrap gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void onFork()
          }}
        >
          <input
            value={forkName}
            onChange={(e) => setForkName(e.target.value)}
            placeholder="Fork folder name (optional)"
            className="h-10 min-w-[14rem] flex-1 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
          />
          <Button type="submit" size="sm" disabled={exportBusy || !projectId}>
            Fork project
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

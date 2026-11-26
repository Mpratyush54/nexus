import { useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Copy, Eye, EyeOff } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { useToast } from '@/components/ui/Toast'
import { useCreateToken, useMe, useRevokeToken, useTokens, useUsage } from '@/hooks/useAuthMutations'
import { authApi, type ApiToken } from '@/api/auth'
import { notificationsApi } from '@/api/notifications'
import { projectsApi, type ExportMemory } from '@/api/projects'
import { formatBytes, storageApi } from '@/api/storage'
import { PlanGrid } from '@/components/PlanGrid'
import { AppearanceSettings } from '@/components/AppearanceSettings'
import { VoiceInputSettings } from '@/components/voice/VoiceInputSettings'
import {
  FieldRow,
  FormMessage,
  LoadError,
  RowSkeleton,
  SettingsNav,
  SettingsSection,
  type SectionLink,
} from '@/components/settings/SettingsKit'
import { inputClass, useActiveSection } from '@/components/settings/settings-utils'
import { SupportSection } from '@/components/settings/SupportSection'
import { subscribeWebPush } from '@/lib/pwa'
import { projectLabel } from '@/lib/project-label'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'
import { useAuth } from '@/providers/AuthProvider'
import { useBillingPlans, useMyBilling } from '@/hooks/useBilling'
import { useCurrentProject } from '@/hooks/useProjects'

const SECTIONS: SectionLink[] = [
  { id: 'profile', label: 'Profile' },
  { id: 'security', label: 'Password' },
  { id: 'appearance', label: 'Appearance' },
  { id: 'plan', label: 'Plan & usage' },
  { id: 'notifications', label: 'Notifications' },
  { id: 'tokens', label: 'API tokens' },
  { id: 'project-data', label: 'Project data' },
  { id: 'support', label: 'Privacy & support' },
]

function errText(err: unknown) {
  return err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'Something went wrong.'
}

/** Copy to clipboard with a short-lived "Copied" state for the button label. */
function useCopy() {
  const [copied, setCopied] = useState<string | null>(null)
  const { push } = useToast()
  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(null), 1800)
    return () => clearTimeout(t)
  }, [copied])
  const copy = (key: string, text: string) =>
    navigator.clipboard
      .writeText(text)
      .then(() => setCopied(key))
      .catch(() => push({ title: 'Copy blocked', detail: 'Your browser blocked the clipboard. Select the text instead.', tone: 'amber' }))
  return { copied, copy }
}

function CopyButton({ label, done, onClick }: { label: string; done: boolean; onClick: () => void }) {
  return (
    <Button type="button" size="sm" variant="secondary" onClick={onClick} aria-label={done ? 'Copied' : label}>
      {done ? <Check className="h-3.5 w-3.5" aria-hidden /> : <Copy className="h-3.5 w-3.5" aria-hidden />}
      {done ? 'Copied' : 'Copy'}
    </Button>
  )
}

/** Only ever show the start of a secret. */
function masked(prefix: string) {
  return `${prefix}${'•'.repeat(12)}`
}

export function SettingsPage() {
  const active = useActiveSection(SECTIONS.map((s) => s.id))
  const me = useMe()

  return (
    <div>
      <header className="mb-6">
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Settings</h1>
        <p className="mt-1 text-sm text-fg-dim">
          Your account, plan, notifications, API tokens and error reports.
          {me.data?.is_platform_admin ? (
            <>
              {' '}
              <Link to="/app/admin" className="text-fg underline decoration-border underline-offset-2 hover:decoration-fg">
                Open the platform console
              </Link>
              .
            </>
          ) : null}
        </p>
      </header>

      <div className="lg:grid lg:grid-cols-[10rem_minmax(0,1fr)] lg:gap-8">
        <SettingsNav sections={SECTIONS} active={active} />
        <div className="min-w-0 space-y-10">
          <ProfileSection />
          <PasswordSection />
          <SettingsSection id="appearance" title="Appearance" description="Applies to the web app in this browser.">
            <FieldRow label="Theme" id="theme" help="“Match system” follows your OS light or dark setting.">
              <AppearanceSettings labelledBy="theme-label" />
            </FieldRow>
            <FieldRow label="Voice input" id="voice" help="Dictate into the chat box with the mic (Ctrl+Shift+Space, or ⌘⇧Space on a Mac). Your browser turns speech into text; Nexus never receives audio.">
              <VoiceInputSettings labelledBy="voice-label" />
            </FieldRow>
          </SettingsSection>
          <PlanSection />
          <NotificationsSection isAdmin={Boolean(me.data?.is_platform_admin)} />
          <TokensSection />
          <ProjectDataSection />
          <SupportSection username={me.data?.username} />
        </div>
      </div>
    </div>
  )
}

function ProfileSection() {
  const me = useMe()
  const qc = useQueryClient()
  const { copied, copy } = useCopy()
  const save = useMutation({
    mutationFn: (next: string) => authApi.updateMe({ email: next }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['users', 'me'] }),
  })

  return (
    <SettingsSection id="profile" title="Profile" description="How teammates find and recognise you.">
      {me.isError ? (
        <div className="p-5">
          <LoadError what="your profile" onRetry={() => void me.refetch()} />
        </div>
      ) : (
        <>
          <FieldRow label="Username" help="Shown to teammates. It can’t be changed.">
            {me.data ? <p className="py-2 font-mono text-sm text-fg">@{me.data.username}</p> : <RowSkeleton />}
          </FieldRow>
          <FieldRow label="User ID" help="Project owners add you with this ID.">
            {me.data ? (
              <div className="flex min-w-0 items-center gap-2">
                <code className="min-w-0 flex-1 truncate rounded-lg border border-border bg-raised px-3 py-2 font-mono text-xs text-fg" title={me.data.id}>
                  {me.data.id}
                </code>
                <CopyButton label="Copy user ID" done={copied === 'uid'} onClick={() => void copy('uid', me.data!.id)} />
              </div>
            ) : (
              <RowSkeleton />
            )}
          </FieldRow>
          <FieldRow label="Email" htmlFor="settings-email" id="settings-email" help="Used to sign in and for password resets.">
            {me.data ? (
              <EmailForm key={me.data.email ?? ''} saved={me.data.email ?? ''} save={save} />
            ) : (
              <RowSkeleton />
            )}
          </FieldRow>
        </>
      )}
    </SettingsSection>
  )
}

type SaveEmail = ReturnType<typeof useMutation<unknown, Error, string>>

/** Keyed on the saved address, so it starts from the server value after each save. */
function EmailForm({ saved, save }: { saved: string; save: SaveEmail }) {
  const [email, setEmail] = useState(saved)
  const trimmed = email.trim()
  const dirty = trimmed !== saved
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        if (dirty && trimmed) save.mutate(trimmed)
      }}
      className="flex flex-wrap items-start gap-2"
    >
      <input
        id="settings-email"
        type="email"
        required
        autoComplete="email"
        value={email}
        onChange={(e) => {
          setEmail(e.target.value)
          if (!save.isPending) save.reset()
        }}
        aria-describedby="settings-email-help"
        className={`${inputClass} min-w-[12rem] flex-1`}
      />
      <Button type="submit" disabled={!dirty || !trimmed || save.isPending}>
        {save.isPending ? 'Saving…' : 'Save'}
      </Button>
      <div className="basis-full">
        {save.isSuccess && !dirty ? <FormMessage kind="ok">Email saved.</FormMessage> : null}
        {save.isError ? <FormMessage kind="error">{errText(save.error)}</FormMessage> : null}
      </div>
    </form>
  )
}

function PasswordSection() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const change = useMutation({
    mutationFn: () => authApi.changePassword({ current_password: current, new_password: next }),
    onSuccess: () => {
      setCurrent('')
      setNext('')
      setConfirm('')
    },
  })
  const mismatch = confirm.length > 0 && confirm !== next
  const tooShort = next.length > 0 && next.length < 8
  const ready = current.length > 0 && next.length >= 8 && confirm === next

  return (
    <SettingsSection id="security" title="Password" description="Change the password you sign in with.">
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (ready) change.mutate()
        }}
        onChange={() => change.isSuccess && change.reset()}
      >
        <FieldRow label="Current password" htmlFor="pw-current">
          <input id="pw-current" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} className={inputClass} />
        </FieldRow>
        <div className="border-t border-border" />
        <FieldRow label="New password" htmlFor="pw-new" id="pw-new" help="At least 8 characters.">
          <input
            id="pw-new"
            type="password"
            autoComplete="new-password"
            required
            minLength={8}
            value={next}
            onChange={(e) => setNext(e.target.value)}
            aria-describedby="pw-new-help"
            aria-invalid={tooShort || undefined}
            className={inputClass}
          />
          {tooShort ? <FormMessage kind="error">Use at least 8 characters.</FormMessage> : null}
        </FieldRow>
        <div className="border-t border-border" />
        <FieldRow label="Confirm new password" htmlFor="pw-confirm">
          <input
            id="pw-confirm"
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            aria-invalid={mismatch || undefined}
            className={inputClass}
          />
          {mismatch ? <FormMessage kind="error">The passwords don’t match.</FormMessage> : null}
        </FieldRow>
        <div className="flex flex-wrap items-center justify-end gap-3 border-t border-border px-5 py-4">
          {change.isSuccess ? <FormMessage kind="ok">Password changed.</FormMessage> : null}
          {change.isError ? <FormMessage kind="error">{errText(change.error)}</FormMessage> : null}
          <Button type="submit" disabled={!ready || change.isPending}>
            {change.isPending ? 'Updating…' : 'Update password'}
          </Button>
        </div>
      </form>
    </SettingsSection>
  )
}

function PlanSection() {
  const plans = useBillingPlans()
  const billing = useMyBilling()
  const usage = useUsage()
  const storage = useQuery({
    queryKey: ['storage', 'usage'],
    queryFn: ({ signal }) => storageApi.usage(undefined, signal),
  })
  const s = storage.data
  const pct = s && s.cap > 0 ? Math.min(100, Math.round((s.used / s.cap) * 100)) : null

  return (
    <SettingsSection id="plan" title="Plan & usage" description="Your personal plan. Paid plans aren’t open yet; the free plan stays free.">
      <div className="p-5">
        <p className="mb-3 text-sm font-medium text-fg">Plans</p>
        {plans.isError ? (
          <LoadError what="plans" onRetry={() => void plans.refetch()} />
        ) : plans.data ? (
          <PlanGrid plans={plans.data} current={billing.data} canChange={false} allowPaidUpgrade={false} />
        ) : (
          <RowSkeleton lines={3} />
        )}
      </div>
      <FieldRow label="Activity" help="Totals for your account.">
        {usage.isError ? (
          <LoadError what="usage" onRetry={() => void usage.refetch()} />
        ) : usage.data ? (
          <dl className="grid grid-cols-3 gap-3">
            {[
              ['Memories', usage.data.memories_created],
              ['Chat summaries', usage.data.episodes_created],
              ['Changes saved', usage.data.requests_approx],
            ].map(([k, v]) => (
              <div key={k} className="rounded-lg border border-border bg-raised/50 px-3 py-2">
                <dt className="text-[11px] text-fg-dim">{k}</dt>
                <dd className="text-lg font-semibold tabular-nums text-fg">{Number(v).toLocaleString()}</dd>
              </div>
            ))}
          </dl>
        ) : (
          <RowSkeleton lines={2} />
        )}
      </FieldRow>
      <FieldRow label="Storage" help="Files your agents upload. Chat history doesn’t count.">
        {storage.isError ? (
          <LoadError what="storage" onRetry={() => void storage.refetch()} />
        ) : s ? (
          <div>
            <p className="text-sm text-fg">
              {formatBytes(s.used)} used{s.cap > 0 ? ` of ${formatBytes(s.cap)}` : ''}
            </p>
            {pct !== null ? (
              <div
                role="progressbar"
                aria-label="Storage used"
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={pct}
                className="mt-2 h-1.5 overflow-hidden rounded-full bg-raised"
              >
                <div
                  className={['h-full rounded-full', s.state === 'full' ? 'bg-danger' : s.state === 'warn' ? 'bg-amber' : 'bg-teal'].join(' ')}
                  style={{ width: `${pct}%` }}
                />
              </div>
            ) : null}
            {s.state === 'full' ? (
              <p className="mt-2 text-xs text-danger">Full: new file uploads are blocked. Transcripts still sync.</p>
            ) : s.state === 'warn' ? (
              <p className="mt-2 text-xs text-amber">Nearly full for your plan.</p>
            ) : null}
          </div>
        ) : (
          <RowSkeleton />
        )}
      </FieldRow>
    </SettingsSection>
  )
}

const PERMISSION_TEXT: Record<string, string> = {
  granted: 'On in this browser.',
  denied: 'Blocked in this browser. Allow notifications for this site in your browser’s site settings, then try again.',
  default: 'Off in this browser.',
  unsupported: 'This browser doesn’t support web push.',
}

function NotificationsSection({ isAdmin }: { isAdmin: boolean }) {
  const { projectId } = useAuth()
  const { push } = useToast()
  const qc = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [permission, setPermission] = useState<string>(
    typeof Notification !== 'undefined' ? Notification.permission : 'unsupported',
  )
  const vapid = useQuery({ queryKey: ['notifications', 'vapid'], queryFn: () => notificationsApi.vapidPublicKey() })
  const devices = useQuery({ queryKey: ['notifications', 'devices'], queryFn: async () => (await notificationsApi.devices()).items ?? [] })
  const configured = Boolean(vapid.data?.configured && vapid.data.public_key)

  const enable = async () => {
    if (!vapid.data?.public_key) return
    setBusy(true)
    try {
      const sub = await subscribeWebPush(vapid.data.public_key)
      if (typeof Notification !== 'undefined') setPermission(Notification.permission)
      if (!sub) return
      const json = sub.toJSON()
      await notificationsApi.subscribe({
        endpoint: json.endpoint!,
        keys: { p256dh: json.keys?.p256dh ?? '', auth: json.keys?.auth ?? '' },
      })
      void qc.invalidateQueries({ queryKey: ['notifications', 'devices'] })
      push({ title: 'Push notifications on', tone: 'teal' })
    } catch (err) {
      push({ title: 'Couldn’t turn on push', detail: errText(err), tone: 'danger' })
    } finally {
      setBusy(false)
    }
  }

  const test = async () => {
    setBusy(true)
    try {
      const res = await notificationsApi.testPush({
        title: 'Nexus test',
        body: 'Notifications on this device are working.',
        url: '/app/settings',
        project_id: projectId ?? undefined,
      })
      push({ title: 'Test sent', detail: `${res.sent} delivered · ${res.failed} failed`, tone: res.sent > 0 ? 'teal' : 'amber' })
    } catch (err) {
      push({ title: 'Test failed', detail: errText(err), tone: 'danger' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <SettingsSection
      id="notifications"
      title="Notifications"
      description={
        <>
          Browser push for handoffs, reviews and mentions. Choose which events notify you in the{' '}
          <Link to="/app/notifications" className="text-fg underline decoration-border underline-offset-2 hover:decoration-fg">
            notifications inbox
          </Link>
          .
        </>
      }
    >
      <FieldRow label="Browser push" help={vapid.isLoading ? undefined : configured ? PERMISSION_TEXT[permission] ?? PERMISSION_TEXT.default : undefined}>
        {vapid.isLoading ? (
          <RowSkeleton />
        ) : vapid.isError ? (
          <LoadError what="push settings" onRetry={() => void vapid.refetch()} />
        ) : !configured ? (
          <p className="text-sm text-fg-dim">
            Push isn’t set up on this Nexus server yet.
            {isAdmin ? ' Set VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY on the server to turn it on.' : ''}
          </p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {permission !== 'granted' ? (
              <Button type="button" disabled={busy || permission === 'denied' || permission === 'unsupported'} onClick={() => void enable()}>
                {busy ? 'Working…' : 'Turn on push'}
              </Button>
            ) : null}
            <Button type="button" variant="secondary" disabled={busy || permission !== 'granted'} onClick={() => void test()}>
              Send a test
            </Button>
          </div>
        )}
      </FieldRow>
      <FieldRow label="Devices" help="Browsers that receive your push notifications.">
        {devices.isLoading ? (
          <RowSkeleton lines={2} />
        ) : devices.isError ? (
          <LoadError what="devices" onRetry={() => void devices.refetch()} />
        ) : devices.data && devices.data.length > 0 ? (
          <>
            <ul className="divide-y divide-border rounded-lg border border-border">
              {devices.data.map((d) => (
                <li key={d.id} className="flex flex-wrap items-baseline justify-between gap-2 px-3 py-2 text-sm">
                  <span className="min-w-0 truncate text-fg">{d.host || 'Browser'}</span>
                  <span className="text-xs text-muted">added {formatRelative(d.created_at)}</span>
                </li>
              ))}
            </ul>
            <Link to="/app/notifications" className="mt-2 inline-block text-xs text-fg-dim underline decoration-border underline-offset-2 hover:text-fg">
              Manage devices
            </Link>
          </>
        ) : (
          <p className="py-2 text-sm text-fg-dim">No devices yet.</p>
        )}
      </FieldRow>
    </SettingsSection>
  )
}

function TokensSection() {
  const tokens = useTokens()
  const createToken = useCreateToken()
  const revokeToken = useRevokeToken()
  const { push } = useToast()
  const { copied, copy } = useCopy()
  const [name, setName] = useState('')
  const [minted, setMinted] = useState<{ name: string; token: string } | null>(null)
  const [reveal, setReveal] = useState(false)
  const [confirming, setConfirming] = useState<string | null>(null)

  const onCreate = (e: FormEvent) => {
    e.preventDefault()
    const tokenName = name.trim() || 'Agent token'
    createToken.mutate(tokenName, {
      onSuccess: (res) => {
        setMinted({ name: tokenName, token: res.token })
        setReveal(false)
        setName('')
      },
    })
  }

  const onRevoke = (t: ApiToken) =>
    revokeToken.mutate(t.id, {
      onSuccess: () => {
        setConfirming(null)
        push({ title: 'Token revoked', detail: t.name })
      },
      onError: (err) => push({ title: 'Couldn’t revoke token', detail: errText(err), tone: 'danger' }),
    })

  return (
    <SettingsSection id="tokens" title="API tokens" description="Let agents, scripts and the CLI act as you. Revoke a token to cut off whatever uses it.">
      <FieldRow label="New token" htmlFor="token-name" id="token-name" help="Name it after what will use it, e.g. “Cursor on laptop”.">
        <form onSubmit={onCreate} className="flex flex-wrap items-start gap-2">
          <input
            id="token-name"
            value={name}
            maxLength={80}
            onChange={(e) => setName(e.target.value)}
            placeholder="Agent token"
            aria-describedby="token-name-help"
            className={`${inputClass} min-w-[12rem] flex-1`}
          />
          <Button type="submit" disabled={createToken.isPending}>
            {createToken.isPending ? 'Creating…' : 'Create token'}
          </Button>
          {createToken.isError ? (
            <div className="basis-full">
              <FormMessage kind="error">{errText(createToken.error)}</FormMessage>
            </div>
          ) : null}
        </form>

        {minted ? (
          <div className="mt-3 rounded-lg border border-amber/40 bg-amber-soft p-3" role="status">
            <p className="text-sm font-medium text-fg">“{minted.name}” created</p>
            <p className="mt-0.5 text-xs text-fg-dim">Copy it now. Nexus won’t show it again.</p>
            <div className="mt-2 flex min-w-0 flex-wrap items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded-md border border-border bg-surface px-2 py-1.5 font-mono text-xs text-fg" aria-label={reveal ? 'Token' : 'Token, hidden'}>
                {reveal ? minted.token : masked(minted.token.slice(0, 6))}
              </code>
              <Button type="button" size="sm" variant="secondary" onClick={() => setReveal((v) => !v)} aria-pressed={reveal}>
                {reveal ? <EyeOff className="h-3.5 w-3.5" aria-hidden /> : <Eye className="h-3.5 w-3.5" aria-hidden />}
                {reveal ? 'Hide' : 'Show'}
              </Button>
              <CopyButton label="Copy token" done={copied === 'minted'} onClick={() => void copy('minted', minted.token)} />
              <Button type="button" size="sm" variant="ghost" onClick={() => setMinted(null)}>
                Done
              </Button>
            </div>
          </div>
        ) : null}
      </FieldRow>

      <FieldRow label="Active tokens" help="Only the first characters are kept, so a lost token can’t be shown again. Create a new one instead.">
        {tokens.isLoading ? (
          <RowSkeleton lines={2} />
        ) : tokens.isError ? (
          <LoadError what="tokens" onRetry={() => void tokens.refetch()} />
        ) : (tokens.data ?? []).length === 0 ? (
          <p className="py-2 text-sm text-fg-dim">No tokens yet.</p>
        ) : (
          <ul className="divide-y divide-border rounded-lg border border-border">
            {(tokens.data ?? []).map((t) => (
              <li key={t.id} className="px-3 py-2.5">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="min-w-0">
                    <p className="truncate text-sm text-fg">{t.name}</p>
                    <p className="font-mono text-[11px] text-muted">
                      {masked(t.token_prefix)} · created {formatRelative(t.created_at)}
                      {t.last_used_at ? ` · used ${formatRelative(t.last_used_at)}` : ' · never used'}
                    </p>
                  </div>
                  {confirming !== t.id ? (
                    <Button type="button" size="sm" variant="secondary" onClick={() => setConfirming(t.id)} aria-label={`Revoke ${t.name}`}>
                      Revoke
                    </Button>
                  ) : null}
                </div>
                {confirming === t.id ? (
                  <div role="alertdialog" aria-labelledby={`revoke-${t.id}`} className="mt-2 rounded-lg border border-danger/30 bg-danger-soft p-3">
                    <p id={`revoke-${t.id}`} className="text-sm text-fg">
                      Revoke “{t.name}”? Anything using it stops working right away. This can’t be undone.
                    </p>
                    <div className="mt-2 flex flex-wrap justify-end gap-2">
                      <Button type="button" size="sm" variant="ghost" onClick={() => setConfirming(null)} autoFocus>
                        Cancel
                      </Button>
                      <Button type="button" size="sm" variant="danger" disabled={revokeToken.isPending} onClick={() => onRevoke(t)}>
                        {revokeToken.isPending ? 'Revoking…' : 'Revoke token'}
                      </Button>
                    </div>
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </FieldRow>
    </SettingsSection>
  )
}

function ProjectDataSection() {
  const { projectId, setProjectId } = useAuth()
  const { current } = useCurrentProject()
  const { push } = useToast()
  const [busy, setBusy] = useState<string | null>(null)
  const [forkName, setForkName] = useState('')
  const name = current ? projectLabel(current) : 'this project'

  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what)
    try {
      await fn()
    } catch (err) {
      push({ title: `${what} failed`, detail: errText(err), tone: 'danger' })
    } finally {
      setBusy(null)
    }
  }

  if (!projectId) {
    return (
      <SettingsSection id="project-data" title="Project data" description="Export, import or fork the active project's memories.">
        <div className="p-5 text-sm text-fg-dim">
          No project is selected.{' '}
          <Link to="/app/dashboard" className="text-fg underline decoration-border underline-offset-2">
            Pick one on Home
          </Link>{' '}
          to export or import its memories.
        </div>
      </SettingsSection>
    )
  }

  return (
    <SettingsSection
      id="project-data"
      title="Project data"
      description={
        <>
          Applies to the active project, <span className="font-medium text-fg">{name}</span>. Switch projects from the sidebar.
        </>
      }
    >
      <FieldRow label="Export memories" id="export" help="Downloads every memory in this project.">
        <div role="group" aria-labelledby="export-label" className="flex flex-wrap gap-2">
          {(['json', 'yaml', 'markdown'] as const).map((f) => (
            <Button
              key={f}
              type="button"
              variant="secondary"
              disabled={busy !== null}
              onClick={() =>
                void run('Export', async () => {
                  await projectsApi.downloadExport(projectId, f)
                  push({ title: 'Export downloaded', detail: `${name} · ${f}`, tone: 'teal' })
                })
              }
            >
              {f === 'json' ? 'JSON' : f === 'yaml' ? 'YAML' : 'Markdown'}
            </Button>
          ))}
        </div>
      </FieldRow>
      <FieldRow label="Import memories" htmlFor="import-file" id="import" help="Adds memories from a Nexus JSON export or a Markdown file to this project.">
        <label
          className={[
            'inline-flex h-10 cursor-pointer items-center rounded-lg border border-border px-4 text-sm font-medium text-fg hover:border-border-strong hover:bg-raised',
            'focus-within:border-amber',
            busy ? 'pointer-events-none opacity-40' : '',
          ].join(' ')}
        >
          {busy === 'Import' ? 'Importing…' : 'Choose file…'}
          <input
            id="import-file"
            type="file"
            accept=".json,.md,application/json,text/markdown"
            aria-describedby="import-help"
            className="sr-only"
            onChange={(e) => {
              const file = e.target.files?.[0]
              e.target.value = ''
              if (!file) return
              void run('Import', async () => {
                const text = await file.text()
                let result
                if (file.name.endsWith('.md') || file.type.includes('markdown')) {
                  result = await projectsApi.importMarkdown(projectId, text)
                } else {
                  const parsed = JSON.parse(text) as { memories?: ExportMemory[] } | ExportMemory[]
                  result = await projectsApi.importMemories(projectId, Array.isArray(parsed) ? parsed : parsed.memories ?? [])
                }
                push({ title: 'Import complete', detail: `${result.imported} imported · ${result.skipped} skipped`, tone: 'teal' })
              })
            }}
          />
        </label>
      </FieldRow>
      <FieldRow label="Copy project" htmlFor="fork-name" id="fork-name" help="Copies this project’s memories into a new project and switches you to it. The original stays as it is.">
        <form
          className="flex flex-wrap gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void run('Fork', async () => {
              const res = await projectsApi.fork(projectId, { folder_name: forkName.trim() || undefined })
              setProjectId(res.project.id)
              setForkName('')
              push({ title: 'Project copied', detail: `${res.memories_copied} memories copied`, tone: 'teal' })
            })
          }}
        >
          <input
            id="fork-name"
            value={forkName}
            onChange={(e) => setForkName(e.target.value)}
            placeholder="New folder name (optional)"
            aria-describedby="fork-name-help"
            className={`${inputClass} min-w-[12rem] flex-1`}
          />
          <Button type="submit" variant="secondary" disabled={busy !== null}>
            {busy === 'Fork' ? 'Copying…' : 'Copy'}
          </Button>
        </form>
      </FieldRow>
    </SettingsSection>
  )
}

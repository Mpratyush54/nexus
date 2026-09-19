import { useMemo, useState, type FormEvent } from 'react'
import { Shield } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import { useMe } from '@/hooks/useAuthMutations'
import {
  useAdminOverview,
  useAdminReleases,
  useAdminUsers,
  usePublishRelease,
  useSetPlatformAdmin,
  useYankRelease,
} from '@/hooks/useAdmin'
import { useAdminAssignPlan, useAdminSubscriptions, useBillingPlans } from '@/hooks/useBilling'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'
import type { ReleaseArtifact } from '@/api/platform'

const APPS = ['api', 'pwa', 'cli', 'daemon', 'desktop'] as const

export function AdminPage() {
  const { push } = useToast()
  const me = useMe()
  const overview = useAdminOverview()
  const users = useAdminUsers()
  const releases = useAdminReleases()
  const setAdmin = useSetPlatformAdmin()
  const publish = usePublishRelease()
  const yank = useYankRelease()
  const billingPlans = useBillingPlans()
  const subs = useAdminSubscriptions()
  const assignPlan = useAdminAssignPlan()

  const [grantId, setGrantId] = useState('')
  const [subOwnerType, setSubOwnerType] = useState('user')
  const [subOwnerId, setSubOwnerId] = useState('')
  const [subPlanId, setSubPlanId] = useState('pro')
  const [app, setApp] = useState('cli')
  const [version, setVersion] = useState('')
  const [channel, setChannel] = useState('stable')
  const [notes, setNotes] = useState('')
  const [winUrl, setWinUrl] = useState('')
  const [macUrl, setMacUrl] = useState('')
  const [linuxUrl, setLinuxUrl] = useState('')

  const isAdmin = Boolean(me.data?.is_platform_admin) || overview.isSuccess
  const forbidden = overview.isError && overview.error instanceof ApiError && overview.error.status === 403

  const artifacts = useMemo(() => {
    const out: ReleaseArtifact[] = []
    if (winUrl.trim()) out.push({ os: 'windows', arch: 'amd64', url: winUrl.trim() })
    if (macUrl.trim()) out.push({ os: 'darwin', arch: 'arm64', url: macUrl.trim() })
    if (linuxUrl.trim()) out.push({ os: 'linux', arch: 'amd64', url: linuxUrl.trim() })
    return out
  }, [winUrl, macUrl, linuxUrl])

  const onGrant = (e: FormEvent) => {
    e.preventDefault()
    const id = grantId.trim()
    if (!id) return
    setAdmin.mutate(
      { id, admin: true },
      {
        onSuccess: () => {
          push({ title: 'Super Admin granted', detail: id, tone: 'teal' })
          setGrantId('')
        },
        onError: (err) =>
          push({
            title: 'Grant failed',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  const onPublish = (e: FormEvent) => {
    e.preventDefault()
    if (!version.trim()) return
    publish.mutate(
      { app, version: version.trim(), channel, notes: notes.trim() || undefined, artifacts },
      {
        onSuccess: (rel) => {
          push({ title: 'Release published', detail: `${rel.app} ${rel.version}`, tone: 'teal' })
          setVersion('')
          setNotes('')
          setWinUrl('')
          setMacUrl('')
          setLinuxUrl('')
        },
        onError: (err) =>
          push({
            title: 'Publish failed',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  if (forbidden || (me.isSuccess && !isAdmin && overview.isError)) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Platform</h1>
        <p className="text-sm text-fg-dim">Super Admin role required to control the platform.</p>
      </div>
    )
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight text-fg">
          <Shield size={20} />
          Platform
        </h1>
        <p className="mt-1 text-sm text-fg-dim">
          Super Admin control: versions, CLI/desktop releases, and who can operate Nexus on AWS.
        </p>
      </div>

      <GlassPanel className="space-y-3 p-5">
        <h2 className="text-sm font-medium text-fg">Overview</h2>
        {overview.data ? (
          <dl className="grid gap-3 sm:grid-cols-3">
            <div>
              <dt className="text-[11px] text-muted">API</dt>
              <dd className="font-mono text-sm text-fg">{overview.data.api.version}</dd>
            </div>
            <div>
              <dt className="text-[11px] text-muted">Users / orgs / projects</dt>
              <dd className="font-mono text-sm text-fg">
                {overview.data.stats.users} / {overview.data.stats.orgs} / {overview.data.stats.projects}
              </dd>
            </div>
            <div>
              <dt className="text-[11px] text-muted">AWS region</dt>
              <dd className="font-mono text-sm text-fg">{overview.data.aws.region || '—'}</dd>
            </div>
            <div>
              <dt className="text-[11px] text-muted">ECS</dt>
              <dd className="font-mono text-xs text-fg-dim">
                {overview.data.aws.ecs_cluster || 'central-memory-cluster'} /{' '}
                {overview.data.aws.ecs_service || 'central-memory-srv'}
              </dd>
            </div>
            <div>
              <dt className="text-[11px] text-muted">Releases bucket</dt>
              <dd className="font-mono text-xs text-fg-dim">
                {overview.data.aws.releases_bucket || 'central-memory-releases'}
              </dd>
            </div>
            <div>
              <dt className="text-[11px] text-muted">Bootstrap</dt>
              <dd className="font-mono text-xs text-fg-dim">{overview.data.bootstrap_env}</dd>
            </div>
          </dl>
        ) : (
          <p className="text-sm text-muted">Loading platform status…</p>
        )}
        <p className="text-xs text-muted">
          PWA build {import.meta.env.VITE_APP_VERSION ?? 'dev'}. Tag a release (`v0.2.0`) to build
          Windows / macOS / Linux CLI + daemon binaries onto GitHub Releases and S3.
        </p>
      </GlassPanel>

      <GlassPanel className="space-y-3 p-5">
        <h2 className="text-sm font-medium text-fg">Super Admins</h2>
        <p className="text-sm text-fg-dim">
          Platform operators. Env-bootstrap names cannot be revoked here. Project OWNER stays
          tenant-scoped.
        </p>
        <ul className="space-y-2">
          {(users.data ?? []).map((u) => (
            <li
              key={u.id}
              className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-raised px-3 py-2"
            >
              <span className="font-mono text-xs text-fg">{u.username || u.id}</span>
              {u.source ? <StatusPill>{u.source}</StatusPill> : null}
              <StatusPill tone={u.is_platform_admin ? 'teal' : 'neutral'}>
                {u.is_platform_admin ? 'super admin' : 'user'}
              </StatusPill>
              {u.is_platform_admin && u.source !== 'env' ? (
                <Button
                  size="sm"
                  variant="ghost"
                  className="ml-auto"
                  disabled={setAdmin.isPending}
                  onClick={() =>
                    setAdmin.mutate(
                      { id: u.id, admin: false },
                      {
                        onSuccess: () => push({ title: 'Revoked', detail: u.username || u.id }),
                        onError: (err) =>
                          push({
                            title: 'Revoke failed',
                            detail: err instanceof ApiError ? err.message : 'Unknown error',
                            tone: 'danger',
                          }),
                      },
                    )
                  }
                >
                  Revoke
                </Button>
              ) : null}
            </li>
          ))}
        </ul>
        <form onSubmit={onGrant} className="flex flex-wrap items-end gap-2">
          <label className="block min-w-[14rem] flex-1">
            <span className="mb-1 block text-xs text-fg-dim">User id</span>
            <input
              value={grantId}
              onChange={(e) => setGrantId(e.target.value)}
              placeholder="user uuid"
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            />
          </label>
          <Button type="submit" size="sm" disabled={setAdmin.isPending}>
            Grant Super Admin
          </Button>
        </form>
      </GlassPanel>

      <GlassPanel className="space-y-3 p-5">
        <h2 className="text-sm font-medium text-fg">App releases</h2>
        <p className="text-sm text-fg-dim">
          Publish a version so `nexus update` and this console share one channel. Artifact URLs can
          be GitHub Release assets or objects in the AWS releases bucket.
        </p>
        <form onSubmit={onPublish} className="grid gap-3 sm:grid-cols-2">
          <label className="block">
            <span className="mb-1 block text-xs text-fg-dim">App</span>
            <select
              value={app}
              onChange={(e) => setApp(e.target.value)}
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            >
              {APPS.map((a) => (
                <option key={a} value={a}>
                  {a}
                </option>
              ))}
            </select>
          </label>
          <label className="block">
            <span className="mb-1 block text-xs text-fg-dim">Version</span>
            <input
              value={version}
              onChange={(e) => setVersion(e.target.value)}
              placeholder="0.2.0"
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            />
          </label>
          <label className="block">
            <span className="mb-1 block text-xs text-fg-dim">Channel</span>
            <input
              value={channel}
              onChange={(e) => setChannel(e.target.value)}
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            />
          </label>
          <label className="block sm:col-span-2">
            <span className="mb-1 block text-xs text-fg-dim">Notes</span>
            <input
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            />
          </label>
          <label className="block">
            <span className="mb-1 block text-xs text-fg-dim">Windows amd64 URL</span>
            <input
              value={winUrl}
              onChange={(e) => setWinUrl(e.target.value)}
              placeholder="https://…"
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            />
          </label>
          <label className="block">
            <span className="mb-1 block text-xs text-fg-dim">macOS arm64 URL</span>
            <input
              value={macUrl}
              onChange={(e) => setMacUrl(e.target.value)}
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            />
          </label>
          <label className="block sm:col-span-2">
            <span className="mb-1 block text-xs text-fg-dim">Linux amd64 URL</span>
            <input
              value={linuxUrl}
              onChange={(e) => setLinuxUrl(e.target.value)}
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            />
          </label>
          <div>
            <Button type="submit" size="sm" disabled={publish.isPending}>
              Publish
            </Button>
          </div>
        </form>
        <ul className="space-y-2">
          {(releases.data ?? []).map((rel) => (
            <li
              key={`${rel.app}-${rel.version}`}
              className="flex flex-wrap items-center gap-2 rounded-lg border border-border px-3 py-2"
            >
              <StatusPill tone={rel.yanked ? 'danger' : 'teal'}>{rel.app}</StatusPill>
              <span className="font-mono text-xs text-fg">{rel.version}</span>
              <span className="text-[11px] text-muted">{rel.channel}</span>
              <span className="text-[11px] text-muted">{formatRelative(rel.published_at)}</span>
              {!rel.yanked ? (
                <Button
                  size="sm"
                  variant="ghost"
                  className="ml-auto"
                  disabled={yank.isPending}
                  onClick={() =>
                    yank.mutate(
                      { app: rel.app, version: rel.version },
                      {
                        onSuccess: () => push({ title: 'Yanked', detail: rel.version }),
                        onError: (err) =>
                          push({
                            title: 'Yank failed',
                            detail: err instanceof ApiError ? err.message : 'Unknown error',
                            tone: 'danger',
                          }),
                      },
                    )
                  }
                >
                  Yank
                </Button>
              ) : (
                <span className="ml-auto text-[11px] text-danger">yanked</span>
              )}
            </li>
          ))}
        </ul>
      </GlassPanel>

      <GlassPanel className="space-y-3 p-5">
        <h2 className="text-sm font-medium text-fg">Subscriptions</h2>
        <p className="text-sm text-fg-dim">
          Assign a plan to a user or org. Provider stays `manual` until Stripe checkout is wired.
        </p>
        <ul className="space-y-2">
          {(subs.data ?? []).map((sub) => (
            <li
              key={sub.id}
              className="flex flex-wrap items-center gap-2 rounded-lg border border-border px-3 py-2"
            >
              <StatusPill>{sub.owner_type}</StatusPill>
              <span className="font-mono text-xs text-fg">{sub.owner_id}</span>
              <StatusPill tone="teal">{sub.plan_id}</StatusPill>
              <span className="text-[11px] text-muted">{sub.status}</span>
            </li>
          ))}
          {!subs.data?.length ? <li className="text-sm text-muted">No subscriptions yet.</li> : null}
        </ul>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            const id = subOwnerId.trim()
            if (!id) return
            assignPlan.mutate(
              { owner_type: subOwnerType, owner_id: id, plan_id: subPlanId },
              {
                onSuccess: (snap) => {
                  push({ title: 'Plan assigned', detail: `${snap.plan.name} → ${id}`, tone: 'teal' })
                  setSubOwnerId('')
                },
                onError: (err) =>
                  push({
                    title: 'Assign failed',
                    detail: err instanceof ApiError ? err.message : 'Unknown error',
                    tone: 'danger',
                  }),
              },
            )
          }}
          className="grid gap-2 sm:grid-cols-4"
        >
          <select
            value={subOwnerType}
            onChange={(e) => setSubOwnerType(e.target.value)}
            className="h-10 rounded-lg border border-border bg-raised px-2 text-xs text-fg outline-none focus:border-amber"
          >
            <option value="user">user</option>
            <option value="org">org</option>
          </select>
          <input
            value={subOwnerId}
            onChange={(e) => setSubOwnerId(e.target.value)}
            placeholder="owner id"
            className="h-10 rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none focus:border-amber sm:col-span-2"
          />
          <select
            value={subPlanId}
            onChange={(e) => setSubPlanId(e.target.value)}
            className="h-10 rounded-lg border border-border bg-raised px-2 text-xs text-fg outline-none focus:border-amber"
          >
            {(billingPlans.data ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
          <div className="sm:col-span-4">
            <Button type="submit" size="sm" disabled={assignPlan.isPending}>
              Assign plan
            </Button>
          </div>
        </form>
      </GlassPanel>

      <GlassPanel className="space-y-2 p-5">
        <h2 className="text-sm font-medium text-fg">Desktop CLI</h2>
        <p className="text-sm text-fg-dim">
          After a GitHub tag `vX.Y.Z` (or publishing URLs here), operators install and update locally:
        </p>
        <pre className="overflow-x-auto rounded-lg border border-border bg-base p-3 font-mono text-[11px] leading-5 text-fg-dim">
          {`nexus version --check
nexus update --yes
nexus daemon install
nexus daemon status`}
        </pre>
      </GlassPanel>
    </div>
  )
}

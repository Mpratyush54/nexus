import { motion } from 'framer-motion'
import { Building2, FolderPlus, UserPlus } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { orgsApi } from '@/api/orgs'
import {
  useAddOrgMember,
  useCreateOrg,
  useCreateOrgProject,
  useOrg,
  useOrgs,
  useRemoveOrgMember,
  useSetOrgMemberRole,
} from '@/hooks/useOrgs'
import { queryKeys } from '@/lib/query-keys'
import { useBillingPlans, useOrgBilling } from '@/hooks/useBilling'
import { PlanGrid } from '@/components/PlanGrid'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'

const MANAGER_ROLES = ['ADMIN', 'MEMBER'] as const

function initials(id: string) {
  const s = id.replace(/^user_/, '').slice(0, 2)
  return (s || '??').toUpperCase()
}

export function OrgPage() {
  const { orgId: orgIdParam } = useParams()
  const navigate = useNavigate()
  const { user, setProjectId } = useAuth()
  const { push } = useToast()
  const orgs = useOrgs()
  const createOrg = useCreateOrg()

  const [selected, setSelected] = useState<string | null>(orgIdParam ?? null)
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [inviteId, setInviteId] = useState('')
  const [inviteRole, setInviteRole] = useState('MEMBER')
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteToken, setInviteToken] = useState('')
  const [folder, setFolder] = useState('')
  const [display, setDisplay] = useState('')
  const [offboardUser, setOffboardUser] = useState('')
  const [offboardReceiver, setOffboardReceiver] = useState('')
  const [offboardPreview, setOffboardPreview] = useState<{
    sessions: number
    grants: number
    tokens: number
  } | null>(null)

  useEffect(() => {
    if (orgIdParam) {
      setSelected(orgIdParam)
      return
    }
    if (!selected && (orgs.data ?? []).length > 0) {
      setSelected(orgs.data![0].id)
    }
  }, [orgIdParam, orgs.data, selected])

  const detail = useOrg(selected)
  const addMember = useAddOrgMember(selected)
  const setRole = useSetOrgMemberRole(selected)
  const remove = useRemoveOrgMember(selected)
  const createProject = useCreateOrgProject(selected)
  const plans = useBillingPlans()

  const myRole = useMemo(() => {
    const uid = user?.userId
    if (!uid) return ''
    return detail.data?.members.find((m) => m.user_id === uid)?.role ?? ''
  }, [detail.data, user?.userId])
  const isOwner = myRole === 'OWNER'
  const canManage = isOwner || myRole === 'ADMIN'
  const orgBilling = useOrgBilling(isOwner ? selected : null)
  const qc = useQueryClient()
  const storage = useQuery({
    queryKey: queryKeys.orgs.storage(selected ?? ''),
    enabled: canManage && Boolean(selected),
    queryFn: () => orgsApi.storage(selected!),
  })
  const audit = useQuery({
    queryKey: queryKeys.orgs.audit(selected ?? ''),
    enabled: Boolean(selected) && Boolean(detail.data),
    queryFn: () => orgsApi.audit(selected!),
  })
  const shares = useQuery({
    queryKey: queryKeys.orgs.shares(selected ?? ''),
    enabled: canManage && Boolean(selected),
    queryFn: () => orgsApi.shares(selected!),
  })
  const capture = useMutation({
    mutationFn: ({ projectId, enabled }: { projectId: string; enabled: boolean }) =>
      orgsApi.setCapture(projectId, enabled),
    onSuccess: () => {
      if (selected) void qc.invalidateQueries({ queryKey: queryKeys.orgs.storage(selected) })
      if (selected) void qc.invalidateQueries({ queryKey: queryKeys.orgs.audit(selected) })
    },
  })
  const revokeShare = useMutation({
    mutationFn: ({ sessionId, userId }: { sessionId: string; userId: string }) =>
      orgsApi.revokeShare(selected!, sessionId, userId),
    onSuccess: () => {
      if (selected) void qc.invalidateQueries({ queryKey: queryKeys.orgs.shares(selected) })
      if (selected) void qc.invalidateQueries({ queryKey: queryKeys.orgs.audit(selected) })
    },
  })
  const previewOffboard = useMutation({
    mutationFn: (userId: string) => orgsApi.offboardPreview(selected!, userId),
    onSuccess: (data) => setOffboardPreview(data),
  })
  const runOffboard = useMutation({
    mutationFn: (input: { from_user_id: string; to_user_id: string }) =>
      orgsApi.offboard(selected!, input),
    onSuccess: () => {
      setOffboardPreview(null)
      setOffboardUser('')
      setOffboardReceiver('')
      if (selected) void qc.invalidateQueries({ queryKey: queryKeys.orgs.audit(selected) })
      if (selected) void qc.invalidateQueries({ queryKey: queryKeys.orgs.storage(selected) })
      if (selected) void qc.invalidateQueries({ queryKey: queryKeys.orgs.shares(selected) })
      if (selected) void qc.invalidateQueries({ queryKey: queryKeys.orgs.detail(selected) })
    },
  })
  const emailInvite = useMutation({
    mutationFn: (input: { email: string; role: string }) => orgsApi.createInvite(selected!, input),
  })
  const assignableRoles = isOwner ? (['OWNER', 'ADMIN', 'MEMBER'] as const) : MANAGER_ROLES

  const onCreateOrg = (e: FormEvent) => {
    e.preventDefault()
    const n = name.trim()
    if (!n) return
    createOrg.mutate(
      { name: n, slug: slug.trim() || undefined },
      {
        onSuccess: (org) => {
          push({ title: 'Organization created', detail: org.name, tone: 'teal' })
          setName('')
          setSlug('')
          setSelected(org.id)
          navigate(`/app/org/${org.id}`)
        },
        onError: (err) =>
          push({
            title: 'Could not create org',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  const onInvite = (e: FormEvent) => {
    e.preventDefault()
    const id = inviteId.trim()
    if (!id || !selected) return
    addMember.mutate(
      { user_id: id, role: inviteRole },
      {
        onSuccess: () => {
          push({ title: 'Member added', detail: id, tone: 'teal' })
          setInviteId('')
        },
        onError: (err) =>
          push({
            title: 'Invite failed',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  const onCreateProject = (e: FormEvent) => {
    e.preventDefault()
    const f = folder.trim()
    if (!f || !selected) return
    createProject.mutate(
      { folder_name: f, display_name: display.trim() || undefined },
      {
        onSuccess: (p) => {
          push({ title: 'Project created', detail: p.display_name || p.folder_name, tone: 'teal' })
          setFolder('')
          setDisplay('')
        },
        onError: (err) =>
          push({
            title: 'Project create failed',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Organization</h1>
        <p className="mt-1 text-sm text-fg-dim">
          Teams above projects — members, roles, and multi-project management.
        </p>
      </div>

      <div className="grid gap-4 lg:grid-cols-[16rem_1fr]">
        <GlassPanel className="space-y-3 p-4">
          <h2 className="text-sm font-medium text-fg">Your orgs</h2>
          <ul className="space-y-1">
            {(orgs.data ?? []).map((o) => (
              <li key={o.id}>
                <button
                  type="button"
                  onClick={() => {
                    setSelected(o.id)
                    navigate(`/app/org/${o.id}`)
                  }}
                  className={[
                    'flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-sm transition',
                    selected === o.id ? 'bg-raised text-fg' : 'text-fg-dim hover:bg-raised/60 hover:text-fg',
                  ].join(' ')}
                >
                  <Building2 className="h-3.5 w-3.5 shrink-0" />
                  <span className="truncate">{o.name}</span>
                </button>
              </li>
            ))}
            {orgs.isLoading ? <li className="px-2 py-3 text-xs text-muted">Loading…</li> : null}
            {!orgs.isLoading && (orgs.data ?? []).length === 0 ? (
              <li className="px-2 py-3 text-xs text-muted">No organizations yet.</li>
            ) : null}
          </ul>

          <form onSubmit={onCreateOrg} className="space-y-2 border-t border-border pt-3">
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="New org name"
              required
              className="h-9 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            />
            <input
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder="slug (optional)"
              className="h-9 w-full rounded-lg border border-border bg-raised px-3 font-mono text-xs text-fg outline-none focus:border-amber"
            />
            <Button type="submit" size="sm" disabled={createOrg.isPending}>
              Create org
            </Button>
          </form>
        </GlassPanel>

        <div className="space-y-4">
          {!selected ? (
            <GlassPanel className="p-6">
              <p className="text-sm text-fg-dim">Create or select an organization to manage members and projects.</p>
            </GlassPanel>
          ) : detail.isLoading ? (
            <p className="text-sm text-muted">Loading organization…</p>
          ) : detail.isError ? (
            <GlassPanel className="p-4">
              <p className="text-sm text-danger">
                {detail.error instanceof ApiError ? detail.error.message : 'Failed to load org'}
              </p>
            </GlassPanel>
          ) : detail.data ? (
            <>
              <GlassPanel className="flex flex-wrap items-start justify-between gap-3 p-5">
                <div>
                  <div className="flex flex-wrap items-center gap-2">
                    <h2 className="text-lg font-medium text-fg">{detail.data.organization.name}</h2>
                    {detail.data.organization.slug ? (
                      <StatusPill>{detail.data.organization.slug}</StatusPill>
                    ) : null}
                    {myRole ? <StatusPill tone={canManage ? 'teal' : 'neutral'}>{myRole}</StatusPill> : null}
                  </div>
                  <p className="mt-1 font-mono text-[11px] text-muted">{detail.data.organization.id}</p>
                </div>
                <p className="text-xs text-muted">
                  created {formatRelative(detail.data.organization.created_at)}
                </p>
              </GlassPanel>

              <GlassPanel className="space-y-4 p-5">
                <div>
                  <h3 className="text-sm font-medium text-fg">Plan</h3>
                  <p className="mt-1 text-xs text-fg-dim">
                    {isOwner
                      ? 'Org subscription. Paid upgrades stay disabled until checkout is available.'
                      : 'Billing is visible to organization owners.'}
                  </p>
                </div>
                {isOwner && plans.data ? (
                  <PlanGrid
                    plans={plans.data}
                    current={orgBilling.data}
                    canChange={false}
                    allowPaidUpgrade={false}
                    onPaidUnavailable={(plan) =>
                      push({
                        title: 'Sorry — not available yet',
                        detail: `${plan.name} is coming soon. Keep enjoying free until paid plans open.`,
                        tone: 'amber',
                      })
                    }
                  />
                ) : null}
              </GlassPanel>

              <GlassPanel className="space-y-4 p-5">
                <div className="flex items-center justify-between gap-2">
                  <h3 className="text-sm font-medium text-fg">Members</h3>
                  <StatusPill tone="accent">{`${detail.data.members.length} people`}</StatusPill>
                </div>
                {canManage ? (
                  <form onSubmit={onInvite} className="flex flex-wrap items-end gap-2">
                    <label className="block min-w-[12rem] flex-1">
                      <span className="mb-1 block text-xs text-fg-dim">Add by user id</span>
                      <input
                        value={inviteId}
                        onChange={(e) => setInviteId(e.target.value)}
                        placeholder="user uuid"
                        className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
                      />
                    </label>
                    <select
                      value={inviteRole}
                      onChange={(e) => setInviteRole(e.target.value)}
                      className="h-10 rounded-lg border border-border bg-raised px-2 text-xs text-fg outline-none focus:border-amber"
                    >
                      {assignableRoles.map((r) => (
                        <option key={r} value={r}>
                          {r}
                        </option>
                      ))}
                    </select>
                    <Button type="submit" size="sm" disabled={addMember.isPending}>
                      <UserPlus className="h-3.5 w-3.5" />
                      Add
                    </Button>
                  </form>
                ) : null}
                {canManage ? (
                  <form
                    onSubmit={(e) => {
                      e.preventDefault()
                      const email = inviteEmail.trim()
                      if (!email || !selected) return
                      emailInvite.mutate(
                        { email, role: inviteRole },
                        {
                          onSuccess: (inv) => {
                            setInviteToken(inv.token ?? '')
                            setInviteEmail('')
                            push({ title: 'Invite created', detail: inv.email, tone: 'teal' })
                          },
                          onError: (err) =>
                            push({
                              title: 'Invite failed',
                              detail: err instanceof ApiError ? err.message : 'Unknown error',
                              tone: 'danger',
                            }),
                        },
                      )
                    }}
                    className="flex flex-wrap items-end gap-2"
                  >
                    <label className="block min-w-[12rem] flex-1">
                      <span className="mb-1 block text-xs text-fg-dim">Invite by email</span>
                      <input
                        value={inviteEmail}
                        onChange={(e) => setInviteEmail(e.target.value)}
                        placeholder="name@company.dev"
                        type="email"
                        className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
                      />
                    </label>
                    <Button type="submit" size="sm" disabled={emailInvite.isPending}>
                      Create link
                    </Button>
                  </form>
                ) : null}
                {inviteToken ? (
                  <p className="break-all font-mono text-[11px] text-fg-dim">Invite token: {inviteToken}</p>
                ) : null}
                {!canManage ? (
                  <p className="text-xs text-muted">Only owners and admins can manage membership.</p>
                ) : null}
                <ul className="divide-y divide-border">
                  {detail.data.members.map((m) => {
                    const isSelf = m.user_id === user?.userId
                    return (
                      <li key={m.user_id} className="flex flex-wrap items-center gap-3 py-3">
                        <div className="app-avatar flex h-9 w-9 items-center justify-center rounded-full bg-raised text-[10px] font-medium">
                          {initials(m.user_id)}
                        </div>
                        <div className="min-w-0 flex-1">
                          <p className="truncate font-mono text-sm text-fg">
                            {m.user_id}
                            {isSelf ? <span className="ml-2 text-xs text-muted">you</span> : null}
                          </p>
                        </div>
                        <select
                          value={m.role}
                          disabled={!canManage || setRole.isPending || (m.role === 'OWNER' && !isOwner)}
                          onChange={(e) =>
                            setRole.mutate(
                              { userId: m.user_id, role: e.target.value },
                              {
                                onSuccess: () => push({ title: 'Role updated', detail: e.target.value }),
                                onError: (err) =>
                                  push({
                                    title: 'Role update failed',
                                    detail: err instanceof ApiError ? err.message : 'Unknown error',
                                    tone: 'danger',
                                  }),
                              },
                            )
                          }
                          className="h-9 rounded-lg border border-border bg-raised px-2 text-xs text-fg outline-none focus:border-amber disabled:opacity-60"
                        >
                          {(m.role === 'OWNER' && !isOwner ? ['OWNER', ...MANAGER_ROLES] : assignableRoles).map((r) => (
                            <option key={r} value={r}>
                              {r}
                            </option>
                          ))}
                        </select>
                        {canManage && !isSelf && (m.role !== 'OWNER' || isOwner) ? (
                          <Button
                            type="button"
                            size="sm"
                            variant="ghost"
                            disabled={remove.isPending}
                            onClick={() =>
                              remove.mutate(m.user_id, {
                                onSuccess: () => push({ title: 'Removed', detail: m.user_id }),
                                onError: (err) =>
                                  push({
                                    title: 'Remove failed',
                                    detail: err instanceof ApiError ? err.message : 'Unknown error',
                                    tone: 'danger',
                                  }),
                              })
                            }
                          >
                            Remove
                          </Button>
                        ) : null}
                      </li>
                    )
                  })}
                </ul>
              </GlassPanel>

              <GlassPanel className="space-y-4 p-5">
                <div className="flex items-center justify-between gap-2">
                  <h3 className="text-sm font-medium text-fg">Projects</h3>
                  <StatusPill>{`${detail.data.projects.length}`}</StatusPill>
                </div>
                {canManage ? (
                  <form onSubmit={onCreateProject} className="flex flex-wrap items-end gap-2">
                    <input
                      value={folder}
                      onChange={(e) => setFolder(e.target.value)}
                      required
                      placeholder="folder_name"
                      className="h-10 min-w-[10rem] flex-1 rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none focus:border-amber"
                    />
                    <input
                      value={display}
                      onChange={(e) => setDisplay(e.target.value)}
                      placeholder="Display name"
                      className="h-10 min-w-[10rem] flex-1 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
                    />
                    <Button type="submit" size="sm" disabled={createProject.isPending}>
                      <FolderPlus className="h-3.5 w-3.5" />
                      Add project
                    </Button>
                  </form>
                ) : null}
                <ul className="space-y-2">
                  {detail.data.projects.map((p, i) => (
                    <motion.li
                      key={p.id}
                      initial={{ opacity: 0, y: 6 }}
                      animate={{ opacity: 1, y: 0 }}
                      transition={{ delay: i * 0.03 }}
                    >
                      <div className="flex items-center gap-2 rounded-lg border border-border bg-raised/40 px-3 py-2.5">
                        <button
                          type="button"
                          onClick={() => {
                            setProjectId(p.id)
                            navigate('/app/memory')
                          }}
                          className="flex min-w-0 flex-1 items-center justify-between gap-3 text-left"
                        >
                          <span>
                            <span className="block text-sm text-fg">{p.display_name || p.folder_name || p.id}</span>
                            <span className="font-mono text-[11px] text-muted">{p.folder_name}</span>
                          </span>
                          <span className="text-xs text-muted">Open →</span>
                        </button>
                        {canManage ? (
                          <Button
                            type="button"
                            size="sm"
                            variant="ghost"
                            disabled={capture.isPending}
                            onClick={() => {
                              const row = storage.data?.projects.find((item) => item.project_id === p.id)
                              const enabled = row ? row.capture_enabled : true
                              capture.mutate(
                                { projectId: p.id, enabled: !enabled },
                                {
                                  onSuccess: () =>
                                    push({
                                      title: enabled ? 'Capture off' : 'Capture on',
                                      detail: p.display_name || p.folder_name,
                                    }),
                                  onError: (err) =>
                                    push({
                                      title: 'Capture update failed',
                                      detail: err instanceof ApiError ? err.message : 'Unknown error',
                                      tone: 'danger',
                                    }),
                                },
                              )
                            }}
                          >
                            {storage.data?.projects.find((item) => item.project_id === p.id)?.capture_enabled === false
                              ? 'Capture off'
                              : 'Capture on'}
                          </Button>
                        ) : null}
                      </div>
                    </motion.li>
                  ))}
                  {detail.data.projects.length === 0 ? (
                    <li className="text-sm text-muted">No projects in this org yet.</li>
                  ) : null}
                </ul>
              </GlassPanel>

              {canManage ? (
                <GlassPanel className="space-y-3 p-5">
                  <h3 className="text-sm font-medium text-fg">Storage</h3>
                  <p className="text-xs text-fg-dim">
                    Private sessions are counts, bytes, and last active time. Titles stay with the owner.
                  </p>
                  <ul className="space-y-1 text-sm text-fg">
                    {(storage.data?.members ?? []).map((row) => (
                      <li key={row.user_id} className="flex justify-between gap-3">
                        <span className="truncate font-mono text-xs">{row.user_id}</span>
                        <span className="text-xs text-fg-dim">
                          {row.session_count} sessions
                          {row.last_active_at ? ` · ${formatRelative(row.last_active_at)}` : ''}
                        </span>
                      </li>
                    ))}
                    {(storage.data?.members ?? []).length === 0 ? (
                      <li className="text-xs text-muted">No captured sessions yet.</li>
                    ) : null}
                  </ul>
                </GlassPanel>
              ) : null}

              {canManage ? (
                <GlassPanel className="space-y-3 p-5">
                  <h3 className="text-sm font-medium text-fg">Shares</h3>
                  <p className="text-xs text-fg-dim">
                    Active person grants across org projects. Session titles are never shown.
                  </p>
                  <ul className="divide-y divide-border">
                    {(shares.data?.items ?? []).map((row) => (
                      <li
                        key={`${row.session_id}-${row.grantee_id}`}
                        className="flex flex-wrap items-center gap-3 py-2.5"
                      >
                        <div className="min-w-0 flex-1 font-mono text-[11px] text-fg-dim">
                          <span className="block truncate text-fg">{row.session_id}</span>
                          <span className="block truncate">
                            {row.owner_id} → {row.grantee_id}
                            {row.live ? ' · live' : ''}
                          </span>
                        </div>
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          disabled={revokeShare.isPending}
                          onClick={() =>
                            revokeShare.mutate(
                              { sessionId: row.session_id, userId: row.grantee_id },
                              {
                                onSuccess: () =>
                                  push({ title: 'Share revoked', detail: row.grantee_id, tone: 'teal' }),
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
                      </li>
                    ))}
                    {(shares.data?.items ?? []).length === 0 ? (
                      <li className="py-2 text-xs text-muted">No active shares.</li>
                    ) : null}
                  </ul>
                </GlassPanel>
              ) : null}

              {canManage ? (
                <GlassPanel className="space-y-3 p-5">
                  <h3 className="text-sm font-medium text-fg">Offboard</h3>
                  <p className="text-xs text-fg-dim">
                    Transfer org session ownership, revoke tokens, and move secret grants. Preview shows
                    counts only.
                  </p>
                  <form
                    onSubmit={(e) => {
                      e.preventDefault()
                      const from = offboardUser.trim()
                      if (!from || !selected) return
                      previewOffboard.mutate(from, {
                        onError: (err) =>
                          push({
                            title: 'Preview failed',
                            detail: err instanceof ApiError ? err.message : 'Unknown error',
                            tone: 'danger',
                          }),
                      })
                    }}
                    className="flex flex-wrap items-end gap-2"
                  >
                    <label className="block min-w-[10rem] flex-1">
                      <span className="mb-1 block text-xs text-fg-dim">Departing user id</span>
                      <input
                        value={offboardUser}
                        onChange={(e) => {
                          setOffboardUser(e.target.value)
                          setOffboardPreview(null)
                        }}
                        placeholder="user uuid"
                        className="h-10 w-full rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none focus:border-amber"
                      />
                    </label>
                    <label className="block min-w-[10rem] flex-1">
                      <span className="mb-1 block text-xs text-fg-dim">Receiver user id</span>
                      <input
                        value={offboardReceiver}
                        onChange={(e) => setOffboardReceiver(e.target.value)}
                        placeholder="user uuid"
                        className="h-10 w-full rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none focus:border-amber"
                      />
                    </label>
                    <Button type="submit" size="sm" disabled={previewOffboard.isPending}>
                      Preview
                    </Button>
                  </form>
                  {offboardPreview ? (
                    <div className="space-y-2 rounded-lg border border-border bg-raised/40 px-3 py-2.5">
                      <p className="text-xs text-fg">
                        {offboardPreview.sessions} sessions · {offboardPreview.grants} grants ·{' '}
                        {offboardPreview.tokens} tokens
                      </p>
                      <Button
                        type="button"
                        size="sm"
                        disabled={
                          runOffboard.isPending ||
                          !offboardUser.trim() ||
                          !offboardReceiver.trim() ||
                          offboardUser.trim() === offboardReceiver.trim()
                        }
                        onClick={() =>
                          runOffboard.mutate(
                            {
                              from_user_id: offboardUser.trim(),
                              to_user_id: offboardReceiver.trim(),
                            },
                            {
                              onSuccess: (res) =>
                                push({
                                  title: 'Offboard complete',
                                  detail: `${res.sessions_transferred} sessions transferred`,
                                  tone: 'teal',
                                }),
                              onError: (err) =>
                                push({
                                  title: 'Offboard failed',
                                  detail: err instanceof ApiError ? err.message : 'Unknown error',
                                  tone: 'danger',
                                }),
                            },
                          )
                        }
                      >
                        Confirm offboard
                      </Button>
                    </div>
                  ) : null}
                </GlassPanel>
              ) : null}

              <GlassPanel className="space-y-3 p-5">
                <h3 className="text-sm font-medium text-fg">Audit</h3>
                <ul className="space-y-1">
                  {(audit.data?.items ?? []).map((ev, i) => (
                    <li key={`${ev.at}-${ev.action}-${i}`} className="text-xs text-fg-dim">
                      <span className="text-fg">{ev.action}</span>
                      {ev.actor_user_id ? ` · ${ev.actor_user_id}` : ''} · {formatRelative(ev.at)}
                    </li>
                  ))}
                  {(audit.data?.items ?? []).length === 0 ? (
                    <li className="text-xs text-muted">No events yet.</li>
                  ) : null}
                </ul>
              </GlassPanel>
            </>
          ) : null}
        </div>
      </div>
    </div>
  )
}

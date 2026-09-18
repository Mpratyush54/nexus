import { motion } from 'framer-motion'
import { Building2, FolderPlus, UserPlus } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import {
  useAddOrgMember,
  useCreateOrg,
  useCreateOrgProject,
  useOrg,
  useOrgs,
  useRemoveOrgMember,
  useSetOrgMemberRole,
} from '@/hooks/useOrgs'
import { useBillingPlans, useOrgBilling, useSetOrgPlan } from '@/hooks/useBilling'
import { PlanGrid } from '@/components/PlanGrid'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'

const ORG_ROLES = ['ADMIN', 'MEMBER'] as const

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
  const [folder, setFolder] = useState('')
  const [display, setDisplay] = useState('')

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
  const orgBilling = useOrgBilling(selected)
  const setOrgPlan = useSetOrgPlan(selected)

  const myRole = useMemo(() => {
    const uid = user?.userId
    if (!uid) return ''
    return detail.data?.members.find((m) => m.user_id === uid)?.role ?? ''
  }, [detail.data, user?.userId])
  const isAdmin = myRole === 'ADMIN'

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
                    {myRole ? <StatusPill tone={isAdmin ? 'teal' : 'neutral'}>{myRole}</StatusPill> : null}
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
                    This org’s subscription. Admins can switch plans now; a payment provider can
                    attach later.
                  </p>
                </div>
                {plans.data ? (
                  <PlanGrid
                    plans={plans.data}
                    current={orgBilling.data}
                    canChange={isAdmin}
                    pending={setOrgPlan.isPending}
                    onSelect={(planId) =>
                      setOrgPlan.mutate(planId, {
                        onSuccess: (snap) =>
                          push({ title: 'Org plan updated', detail: snap.plan.name, tone: 'teal' }),
                        onError: (err) =>
                          push({
                            title: 'Plan change failed',
                            detail: err instanceof ApiError ? err.message : 'Unknown error',
                            tone: 'danger',
                          }),
                      })
                    }
                  />
                ) : (
                  <p className="text-sm text-muted">Loading plans…</p>
                )}
              </GlassPanel>

              <GlassPanel className="space-y-4 p-5">
                <div className="flex items-center justify-between gap-2">
                  <h3 className="text-sm font-medium text-fg">Members</h3>
                  <StatusPill tone="accent">{`${detail.data.members.length} people`}</StatusPill>
                </div>
                {isAdmin ? (
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
                      {ORG_ROLES.map((r) => (
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
                ) : (
                  <p className="text-xs text-muted">Only org admins can manage membership.</p>
                )}
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
                          disabled={!isAdmin || setRole.isPending}
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
                          {ORG_ROLES.map((r) => (
                            <option key={r} value={r}>
                              {r}
                            </option>
                          ))}
                        </select>
                        {isAdmin && !isSelf ? (
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
                {isAdmin ? (
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
                      <button
                        type="button"
                        onClick={() => {
                          setProjectId(p.id)
                          navigate('/app/memory')
                        }}
                        className="flex w-full items-center justify-between gap-3 rounded-lg border border-border bg-raised/40 px-3 py-2.5 text-left transition hover:border-border-strong"
                      >
                        <span>
                          <span className="block text-sm text-fg">{p.display_name || p.folder_name || p.id}</span>
                          <span className="font-mono text-[11px] text-muted">{p.folder_name}</span>
                        </span>
                        <span className="text-xs text-muted">Open →</span>
                      </button>
                    </motion.li>
                  ))}
                  {detail.data.projects.length === 0 ? (
                    <li className="text-sm text-muted">No projects in this org yet.</li>
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

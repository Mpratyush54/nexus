import { Building2, Copy, FolderPlus, KeyRound, Plus, RefreshCw, UserPlus } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { PageHeader } from '@/components/PageHeader'
import { TeamTabs } from '@/components/TeamTabs'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import { orgsApi, type OrgInvite } from '@/api/orgs'
import {
  useAddOrgMember,
  useCreateOrg,
  useCreateOrgProject,
  useOrg,
  useOrgs,
  useRemoveOrgMember,
  useSetOrgMemberRole,
} from '@/hooks/useOrgs'
import { initialsOf, usePeople } from '@/hooks/usePeople'
import { queryKeys } from '@/lib/query-keys'
import { MEMBER_ID_HELP, memberIdProblem, normalizeMemberId } from '@/lib/member-id'
import { projectLabel, projectSubtitle } from '@/lib/project-label'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'

const ROLE_HELP: Record<string, string> = {
  OWNER: 'Full control, including other owners',
  ADMIN: 'Adds people and projects',
  MEMBER: 'Opens the organization’s projects',
}

const inputClass =
  'h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber'

function errorText(err: unknown, fallback = 'Something went wrong') {
  if (err instanceof ApiError) {
    if (err.status === 403) return 'You don’t have access to this. Ask an owner or admin of the organization.'
    if (err.status === 404) return 'Not found. It may have been removed.'
    if (err.status === 501) return 'This server doesn’t support organizations yet.'
    return err.message
  }
  return err instanceof Error ? err.message : fallback
}

function Section({
  title,
  description,
  aside,
  children,
}: {
  title: string
  description?: string
  aside?: ReactNode
  children: ReactNode
}) {
  return (
    <GlassPanel className="space-y-4 p-5">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-sm font-medium text-fg">{title}</h2>
          {description ? <p className="mt-0.5 text-xs text-fg-dim">{description}</p> : null}
        </div>
        {aside}
      </div>
      {children}
    </GlassPanel>
  )
}

export function OrgPage() {
  const { orgId: orgIdParam } = useParams()
  const navigate = useNavigate()
  const { push } = useToast()
  const orgs = useOrgs()
  const [selected, setSelected] = useState<string | null>(orgIdParam ?? null)
  const [panel, setPanel] = useState<'none' | 'create' | 'join'>('none')

  useEffect(() => {
    if (orgIdParam) {
      setSelected(orgIdParam)
      return
    }
    const list = orgs.data ?? []
    if ((!selected || !list.some((o) => o.id === selected)) && list.length > 0) {
      setSelected(list[0].id)
    }
  }, [orgIdParam, orgs.data, selected])

  const list = orgs.data ?? []
  const choose = (id: string) => {
    setSelected(id)
    navigate(`/app/org/${id}`)
  }

  return (
    <div className="space-y-8">
      <TeamTabs />
      <PageHeader
        title="Team & org"
        description="An organization groups people and projects, so everyone in it can open the same chats and memory."
        actions={
          <>
            <Button
              type="button"
              size="sm"
              variant={panel === 'join' ? 'primary' : 'secondary'}
              onClick={() => setPanel((p) => (p === 'join' ? 'none' : 'join'))}
            >
              <KeyRound className="h-3.5 w-3.5" />
              Join with a code
            </Button>
            <Button
              type="button"
              size="sm"
              variant={panel === 'create' ? 'primary' : 'secondary'}
              onClick={() => setPanel((p) => (p === 'create' ? 'none' : 'create'))}
            >
              <Plus className="h-3.5 w-3.5" />
              New organization
            </Button>
          </>
        }
      >
        {list.length > 1 ? (
          <label className="flex max-w-sm items-center gap-2">
            <span className="text-xs text-fg-dim">Organization</span>
            <select
              value={selected ?? ''}
              onChange={(e) => choose(e.target.value)}
              className="h-9 flex-1 rounded-lg border border-border bg-raised px-2 text-sm text-fg outline-none focus:border-amber"
            >
              {list.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.name}
                </option>
              ))}
            </select>
          </label>
        ) : null}
      </PageHeader>

      {panel === 'create' ? (
        <CreateOrgForm
          onDone={(id) => {
            setPanel('none')
            choose(id)
            push({ title: 'Organization created', tone: 'teal' })
          }}
          onCancel={() => setPanel('none')}
        />
      ) : null}
      {panel === 'join' ? (
        <JoinOrgForm
          onDone={(orgId) => {
            setPanel('none')
            choose(orgId)
          }}
          onCancel={() => setPanel('none')}
        />
      ) : null}

      {orgs.isLoading ? (
        <GlassPanel className="p-6">
          <p className="text-sm text-muted" role="status">
            Loading your organizations…
          </p>
        </GlassPanel>
      ) : orgs.isError ? (
        <GlassPanel className="flex flex-wrap items-center justify-between gap-3 p-5">
          <p className="text-sm text-danger">Couldn’t load organizations. {errorText(orgs.error)}</p>
          <Button type="button" size="sm" variant="secondary" onClick={() => void orgs.refetch()}>
            <RefreshCw className="h-3.5 w-3.5" />
            Try again
          </Button>
        </GlassPanel>
      ) : list.length === 0 ? (
        <GlassPanel className="space-y-3 p-6">
          <Building2 className="h-5 w-5 text-amber" aria-hidden />
          <h2 className="text-base font-medium text-fg">You’re not in an organization yet</h2>
          <p className="max-w-lg text-sm text-fg-dim">
            Your own projects work without one. Create an organization to share projects with teammates, or
            paste the code from an invite email to join one.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button type="button" size="sm" onClick={() => setPanel('create')}>
              New organization
            </Button>
            <Button type="button" size="sm" variant="secondary" onClick={() => setPanel('join')}>
              Join with a code
            </Button>
          </div>
        </GlassPanel>
      ) : selected ? (
        <OrgDetail orgId={selected} />
      ) : null}
    </div>
  )
}

function CreateOrgForm({ onDone, onCancel }: { onDone: (id: string) => void; onCancel: () => void }) {
  const createOrg = useCreateOrg()
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const submit = (e: FormEvent) => {
    e.preventDefault()
    const n = name.trim()
    if (!n) return
    createOrg.mutate({ name: n, slug: slug.trim() || undefined }, { onSuccess: (org) => onDone(org.id) })
  }
  return (
    <GlassPanel className="p-5">
      <form onSubmit={submit} className="grid gap-3 sm:grid-cols-[1fr_12rem_auto] sm:items-end">
        <label className="block">
          <span className="mb-1 block text-xs text-fg-dim">Name</span>
          <input value={name} onChange={(e) => setName(e.target.value)} required className={inputClass} placeholder="Acme Labs" />
        </label>
        <label className="block">
          <span className="mb-1 block text-xs text-fg-dim">Short name (optional)</span>
          <input value={slug} onChange={(e) => setSlug(e.target.value)} className={`${inputClass} font-mono`} placeholder="acme" />
        </label>
        <div className="flex gap-2">
          <Button type="submit" size="sm" disabled={createOrg.isPending}>
            {createOrg.isPending ? 'Creating…' : 'Create'}
          </Button>
          <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </form>
      {createOrg.isError ? <p className="mt-2 text-xs text-danger">{errorText(createOrg.error)}</p> : null}
    </GlassPanel>
  )
}

function JoinOrgForm({ onDone, onCancel }: { onDone: (orgId: string) => void; onCancel: () => void }) {
  const qc = useQueryClient()
  const { push } = useToast()
  const [code, setCode] = useState('')
  const accept = useMutation({
    mutationFn: (token: string) => orgsApi.acceptInvite(token),
    onSuccess: (member) => {
      void qc.invalidateQueries({ queryKey: queryKeys.orgs.all })
      void qc.invalidateQueries({ queryKey: queryKeys.project.list })
      push({ title: 'You joined the organization', detail: `Role: ${member.role}`, tone: 'teal' })
      onDone(member.org_id)
    },
  })
  const failure = accept.error instanceof ApiError
    ? accept.error.status === 410
      ? 'This invite has expired. Ask for a new one.'
      : accept.error.status === 404
        ? 'That code doesn’t match an invite. Check it was copied in full.'
        : accept.error.status === 409
          ? 'This invite was already used.'
          : accept.error.message
    : accept.error
      ? errorText(accept.error)
      : null
  return (
    <GlassPanel className="p-5">
      <form
        onSubmit={(e) => {
          e.preventDefault()
          const t = code.trim()
          if (t) accept.mutate(t)
        }}
        className="grid gap-3 sm:grid-cols-[1fr_auto] sm:items-end"
      >
        <label className="block">
          <span className="mb-1 block text-xs text-fg-dim">Invite code from your email</span>
          <input
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
            autoComplete="off"
            spellCheck={false}
            className={`${inputClass} font-mono`}
            placeholder="Paste the code"
          />
        </label>
        <div className="flex gap-2">
          <Button type="submit" size="sm" disabled={accept.isPending}>
            {accept.isPending ? 'Joining…' : 'Join'}
          </Button>
          <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </form>
      {failure ? <p className="mt-2 text-xs text-danger">{failure}</p> : null}
    </GlassPanel>
  )
}

function OrgDetail({ orgId }: { orgId: string }) {
  const { user } = useAuth()
  const detail = useOrg(orgId)

  const myRole = useMemo(() => {
    const uid = user?.userId
    if (!uid) return ''
    return detail.data?.members.find((m) => m.user_id === uid)?.role ?? ''
  }, [detail.data, user?.userId])
  const isOwner = myRole === 'OWNER'
  const canManage = isOwner || myRole === 'ADMIN'

  if (detail.isLoading) {
    return (
      <GlassPanel className="p-6">
        <p className="text-sm text-muted" role="status">
          Loading organization…
        </p>
      </GlassPanel>
    )
  }
  if (detail.isError || !detail.data) {
    return (
      <GlassPanel className="flex flex-wrap items-center justify-between gap-3 p-5">
        <p className="text-sm text-danger">Couldn’t open this organization. {errorText(detail.error)}</p>
        <Button type="button" size="sm" variant="secondary" onClick={() => void detail.refetch()}>
          <RefreshCw className="h-3.5 w-3.5" />
          Try again
        </Button>
      </GlassPanel>
    )
  }

  const org = detail.data.organization
  return (
    <div className="space-y-4">
      <GlassPanel className="flex flex-wrap items-center justify-between gap-3 p-5">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="truncate text-lg font-medium text-fg">{org.name}</h2>
            {myRole ? (
              <StatusPill tone={canManage ? 'teal' : 'neutral'}>{`You: ${myRole.toLowerCase()}`}</StatusPill>
            ) : null}
          </div>
          <p className="mt-1 text-xs text-muted">
            {detail.data.members.length} {detail.data.members.length === 1 ? 'person' : 'people'} ·{' '}
            {detail.data.projects.length} {detail.data.projects.length === 1 ? 'project' : 'projects'}
            {org.created_at ? ` · created ${formatRelative(org.created_at)}` : ''}
          </p>
        </div>
      </GlassPanel>

      <MembersSection orgId={orgId} members={detail.data.members} isOwner={isOwner} canManage={canManage} />
      {canManage ? <InvitesSection orgId={orgId} isOwner={isOwner} /> : null}
      <ProjectsSection orgId={orgId} projects={detail.data.projects} canManage={canManage} />
      <AdminTools orgId={orgId} canManage={canManage} />
    </div>
  )
}

function MembersSection({
  orgId,
  members,
  isOwner,
  canManage,
}: {
  orgId: string
  members: { user_id: string; role: string; created_at?: string }[]
  isOwner: boolean
  canManage: boolean
}) {
  const { user } = useAuth()
  const { nameFor } = usePeople()
  const { push } = useToast()
  const setRole = useSetOrgMemberRole(orgId)
  const remove = useRemoveOrgMember(orgId)
  const addMember = useAddOrgMember(orgId)
  const [memberId, setMemberId] = useState('')
  const [role, setNewRole] = useState('MEMBER')
  const assignable = isOwner ? ['OWNER', 'ADMIN', 'MEMBER'] : ['ADMIN', 'MEMBER']

  const onAdd = (e: FormEvent) => {
    e.preventDefault()
    const id = normalizeMemberId(memberId)
    if (!id) return
    const problem = memberIdProblem(id, { emailInviteHere: true })
    if (problem) {
      push({ ...problem, tone: 'amber' })
      return
    }
    addMember.mutate(
      { user_id: id, role },
      {
        onSuccess: () => {
          push({ title: 'Member added', detail: nameFor(id), tone: 'teal' })
          setMemberId('')
        },
        onError: (err) => push({ title: 'Couldn’t add member', detail: errorText(err), tone: 'danger' }),
      },
    )
  }

  return (
    <Section
      title="People"
      description={canManage ? 'Change roles or remove people. Invite new people by email below.' : 'Only owners and admins can change membership.'}
      aside={<StatusPill tone="accent">{`${members.length}`}</StatusPill>}
    >
      <ul className="divide-y divide-border">
        {members.map((m) => {
          const isSelf = m.user_id === user?.userId
          const label = nameFor(m.user_id, { you: false })
          const lockOwner = m.role === 'OWNER' && !isOwner
          return (
            <li key={m.user_id} className="flex flex-wrap items-center gap-3 py-3">
              <div className="app-avatar flex h-9 w-9 items-center justify-center rounded-full bg-raised text-[10px] font-medium">
                {initialsOf(label)}
              </div>
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm text-fg" title={m.user_id}>
                  {label}
                  {isSelf ? <span className="ml-2 text-xs text-muted">(you)</span> : null}
                </p>
                <p className="text-[11px] text-muted">{ROLE_HELP[m.role] ?? m.role}</p>
              </div>
              {canManage && !lockOwner && !isSelf ? (
                <select
                  aria-label={`Role for ${label}`}
                  value={m.role}
                  disabled={setRole.isPending}
                  onChange={(e) =>
                    setRole.mutate(
                      { userId: m.user_id, role: e.target.value },
                      {
                        onSuccess: () => push({ title: 'Role updated', detail: `${label}: ${e.target.value.toLowerCase()}` }),
                        onError: (err) => push({ title: 'Couldn’t change role', detail: errorText(err), tone: 'danger' }),
                      },
                    )
                  }
                  className="h-9 rounded-lg border border-border bg-raised px-2 text-xs text-fg outline-none focus:border-amber"
                >
                  {assignable.map((r) => (
                    <option key={r} value={r}>
                      {r.toLowerCase()}
                    </option>
                  ))}
                </select>
              ) : (
                <StatusPill>{m.role.toLowerCase()}</StatusPill>
              )}
              {canManage && !isSelf && !lockOwner ? (
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={remove.isPending}
                  onClick={() => {
                    if (!window.confirm(`Remove ${label} from this organization?`)) return
                    remove.mutate(m.user_id, {
                      onSuccess: () => push({ title: 'Removed', detail: label }),
                      onError: (err) => push({ title: 'Couldn’t remove', detail: errorText(err), tone: 'danger' }),
                    })
                  }}
                >
                  Remove
                </Button>
              ) : null}
            </li>
          )
        })}
      </ul>
      {canManage ? (
        <details className="rounded-lg border border-border/70 px-3 py-2">
          <summary className="cursor-pointer text-xs text-fg-dim">Add someone by user ID instead</summary>
          <form onSubmit={onAdd} className="mt-3 flex flex-wrap items-end gap-2">
            <label className="block min-w-0 flex-1">
              <span className="mb-1 block text-xs text-fg-dim">User ID</span>
              <input
                value={memberId}
                onChange={(e) => setMemberId(e.target.value)}
                placeholder="1b2c3d4e-5f60-…"
                autoComplete="off"
                spellCheck={false}
                className={`${inputClass} font-mono`}
              />
              <span className="mt-1 block text-[11px] text-muted">{MEMBER_ID_HELP}</span>
            </label>
            <select
              aria-label="Role"
              value={role}
              onChange={(e) => setNewRole(e.target.value)}
              className="h-10 rounded-lg border border-border bg-raised px-2 text-xs text-fg outline-none focus:border-amber"
            >
              {assignable.map((r) => (
                <option key={r} value={r}>
                  {r.toLowerCase()}
                </option>
              ))}
            </select>
            <Button type="submit" size="sm" disabled={addMember.isPending}>
              <UserPlus className="h-3.5 w-3.5" />
              Add
            </Button>
          </form>
        </details>
      ) : null}
    </Section>
  )
}

function pendingInvite(inv: OrgInvite) {
  if (inv.accepted_at || inv.revoked_at) return false
  if (inv.expires_at && new Date(inv.expires_at).getTime() < Date.now()) return false
  return true
}

function InvitesSection({ orgId, isOwner }: { orgId: string; isOwner: boolean }) {
  const qc = useQueryClient()
  const { push } = useToast()
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('MEMBER')
  const [fresh, setFresh] = useState<OrgInvite | null>(null)
  const invites = useQuery({
    queryKey: queryKeys.orgs.invites(orgId),
    queryFn: () => orgsApi.listInvites(orgId),
  })
  const create = useMutation({
    mutationFn: (input: { email: string; role: string }) => orgsApi.createInvite(orgId, input),
    onSuccess: (inv) => {
      setFresh(inv)
      setEmail('')
      void qc.invalidateQueries({ queryKey: queryKeys.orgs.invites(orgId) })
    },
  })
  const revoke = useMutation({
    mutationFn: (id: string) => orgsApi.revokeInvite(orgId, id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: queryKeys.orgs.invites(orgId) }),
  })
  const pending = (invites.data?.items ?? []).filter(pendingInvite)
  const roles = isOwner ? ['OWNER', 'ADMIN', 'MEMBER'] : ['ADMIN', 'MEMBER']

  return (
    <Section
      title="Invites"
      description="Invite by email. They get a code by email and paste it under “Join with a code”."
      aside={pending.length ? <StatusPill tone="amber">{`${pending.length} pending`}</StatusPill> : null}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          const v = email.trim()
          if (v) create.mutate({ email: v, role })
        }}
        className="flex flex-wrap items-end gap-2"
      >
        <label className="block min-w-0 flex-1">
          <span className="mb-1 block text-xs text-fg-dim">Email</span>
          <input
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            type="email"
            required
            placeholder="name@company.dev"
            className={inputClass}
          />
        </label>
        <select
          aria-label="Role"
          value={role}
          onChange={(e) => setRole(e.target.value)}
          className="h-10 rounded-lg border border-border bg-raised px-2 text-xs text-fg outline-none focus:border-amber"
        >
          {roles.map((r) => (
            <option key={r} value={r}>
              {r.toLowerCase()}
            </option>
          ))}
        </select>
        <Button type="submit" size="sm" disabled={create.isPending}>
          {create.isPending ? 'Sending…' : 'Send invite'}
        </Button>
      </form>
      {create.isError ? <p className="text-xs text-danger">{errorText(create.error)}</p> : null}
      {fresh?.token ? (
        <div className="space-y-1 rounded-lg border border-teal/30 bg-teal/5 px-3 py-2.5">
          <p className="text-xs text-fg">
            Invite created for {fresh.email}. If the email doesn’t arrive, send them this code:
          </p>
          <div className="flex items-center gap-2">
            <code className="min-w-0 flex-1 break-all font-mono text-[11px] text-fg-dim">{fresh.token}</code>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              aria-label="Copy invite code"
              onClick={() => {
                void navigator.clipboard?.writeText(fresh.token ?? '').then(
                  () => push({ title: 'Code copied', tone: 'teal' }),
                  () => push({ title: 'Couldn’t copy', detail: 'Select the code and copy it by hand.', tone: 'amber' }),
                )
              }}
            >
              <Copy className="h-3.5 w-3.5" />
            </Button>
          </div>
        </div>
      ) : null}

      {invites.isLoading ? (
        <p className="text-xs text-muted">Loading invites…</p>
      ) : invites.isError ? (
        <p className="text-xs text-danger">Couldn’t load invites. {errorText(invites.error)}</p>
      ) : pending.length === 0 ? (
        <p className="text-xs text-muted">No pending invites.</p>
      ) : (
        <ul className="divide-y divide-border">
          {pending.map((inv) => (
            <li key={inv.id} className="flex flex-wrap items-center gap-3 py-2.5">
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm text-fg">{inv.email}</p>
                <p className="text-[11px] text-muted">
                  {inv.role.toLowerCase()}
                  {inv.created_at ? ` · sent ${formatRelative(inv.created_at)}` : ''}
                  {inv.expires_at ? ` · expires ${new Date(inv.expires_at).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}` : ''}
                </p>
              </div>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                disabled={revoke.isPending}
                onClick={() =>
                  revoke.mutate(inv.id, {
                    onSuccess: () => push({ title: 'Invite cancelled', detail: inv.email }),
                    onError: (err) => push({ title: 'Couldn’t cancel', detail: errorText(err), tone: 'danger' }),
                  })
                }
              >
                Cancel invite
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

function ProjectsSection({
  orgId,
  projects,
  canManage,
}: {
  orgId: string
  projects: { id: string; folder_name?: string; display_name?: string; canonical_url?: string }[]
  canManage: boolean
}) {
  const { setProjectId } = useAuth()
  const navigate = useNavigate()
  const { push } = useToast()
  const qc = useQueryClient()
  const createProject = useCreateOrgProject(orgId)
  const [folder, setFolder] = useState('')
  const [display, setDisplay] = useState('')
  const [adding, setAdding] = useState(false)
  const storage = useQuery({
    queryKey: queryKeys.orgs.storage(orgId),
    enabled: canManage,
    queryFn: () => orgsApi.storage(orgId),
  })
  const capture = useMutation({
    mutationFn: ({ projectId, enabled }: { projectId: string; enabled: boolean }) => orgsApi.setCapture(projectId, enabled),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.orgs.storage(orgId) })
      void qc.invalidateQueries({ queryKey: queryKeys.orgs.audit(orgId) })
    },
  })

  return (
    <Section
      title="Projects"
      description="Everyone in the organization can open these projects."
      aside={
        canManage ? (
          <Button type="button" size="sm" variant="secondary" onClick={() => setAdding((v) => !v)}>
            <FolderPlus className="h-3.5 w-3.5" />
            Add project
          </Button>
        ) : null
      }
    >
      {adding ? (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            const f = folder.trim()
            if (!f) return
            createProject.mutate(
              { folder_name: f, display_name: display.trim() || undefined },
              {
                onSuccess: (p) => {
                  push({ title: 'Project added', detail: projectLabel(p), tone: 'teal' })
                  setFolder('')
                  setDisplay('')
                  setAdding(false)
                  void qc.invalidateQueries({ queryKey: queryKeys.project.list })
                },
                onError: (err) => push({ title: 'Couldn’t add project', detail: errorText(err), tone: 'danger' }),
              },
            )
          }}
          className="grid gap-2 sm:grid-cols-[1fr_1fr_auto] sm:items-end"
        >
          <label className="block">
            <span className="mb-1 block text-xs text-fg-dim">Folder name (as on disk)</span>
            <input value={folder} onChange={(e) => setFolder(e.target.value)} required placeholder="my-repo" className={`${inputClass} font-mono`} />
          </label>
          <label className="block">
            <span className="mb-1 block text-xs text-fg-dim">Display name (optional)</span>
            <input value={display} onChange={(e) => setDisplay(e.target.value)} placeholder="My Repo" className={inputClass} />
          </label>
          <Button type="submit" size="sm" disabled={createProject.isPending}>
            {createProject.isPending ? 'Adding…' : 'Add'}
          </Button>
        </form>
      ) : null}
      {projects.length === 0 ? (
        <p className="text-sm text-muted">
          No projects in this organization yet.{canManage ? ' Add one, or move work here from Desktop.' : ''}
        </p>
      ) : (
        <ul className="space-y-2">
          {projects.map((p) => {
            const row = storage.data?.projects?.find((item) => item.project_id === p.id)
            const captureOn = row ? row.capture_enabled : true
            return (
              <li key={p.id} className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-raised/40 px-3 py-2.5">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm text-fg">{projectLabel(p)}</p>
                  <p className="truncate font-mono text-[11px] text-muted">
                    {projectSubtitle(p)}
                    {row ? ` · ${row.session_count} chats` : ''}
                  </p>
                </div>
                {canManage && row ? (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    title="Whether Desktop saves chats from this project"
                    disabled={capture.isPending}
                    onClick={() =>
                      capture.mutate(
                        { projectId: p.id, enabled: !captureOn },
                        {
                          onSuccess: () => push({ title: captureOn ? 'Chat saving off' : 'Chat saving on', detail: projectLabel(p) }),
                          onError: (err) => push({ title: 'Couldn’t change', detail: errorText(err), tone: 'danger' }),
                        },
                      )
                    }
                  >
                    {captureOn ? 'Saving chats' : 'Not saving chats'}
                  </Button>
                ) : null}
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  onClick={() => {
                    setProjectId(p.id)
                    navigate('/app/memory')
                  }}
                >
                  Open memory
                </Button>
              </li>
            )
          })}
        </ul>
      )}
    </Section>
  )
}

/** Rare, admin-only tools stay folded away so the page reads as people + projects. */
function AdminTools({ orgId, canManage }: { orgId: string; canManage: boolean }) {
  const { nameFor } = usePeople()
  const { push } = useToast()
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [offboardUser, setOffboardUser] = useState('')
  const [offboardReceiver, setOffboardReceiver] = useState('')
  const [preview, setPreview] = useState<{ sessions: number; grants: number; tokens: number } | null>(null)
  const audit = useQuery({
    queryKey: queryKeys.orgs.audit(orgId),
    enabled: open,
    queryFn: () => orgsApi.audit(orgId),
  })
  const shares = useQuery({
    queryKey: queryKeys.orgs.shares(orgId),
    enabled: open && canManage,
    queryFn: () => orgsApi.shares(orgId),
  })
  const storage = useQuery({
    queryKey: queryKeys.orgs.storage(orgId),
    enabled: open && canManage,
    queryFn: () => orgsApi.storage(orgId),
  })
  const revokeShare = useMutation({
    mutationFn: ({ sessionId, userId }: { sessionId: string; userId: string }) => orgsApi.revokeShare(orgId, sessionId, userId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.orgs.shares(orgId) })
      void qc.invalidateQueries({ queryKey: queryKeys.orgs.audit(orgId) })
    },
  })
  const previewOffboard = useMutation({
    mutationFn: (userId: string) => orgsApi.offboardPreview(orgId, userId),
    onSuccess: (data) => setPreview(data),
  })
  const runOffboard = useMutation({
    mutationFn: (input: { from_user_id: string; to_user_id: string }) => orgsApi.offboard(orgId, input),
    onSuccess: () => {
      setPreview(null)
      setOffboardUser('')
      setOffboardReceiver('')
      void qc.invalidateQueries({ queryKey: queryKeys.orgs.all })
    },
  })

  return (
    <details
      className="rounded-2xl border border-border bg-surface/40 px-5 py-4"
      onToggle={(e) => setOpen((e.currentTarget as HTMLDetailsElement).open)}
    >
      <summary className="cursor-pointer text-sm font-medium text-fg">
        {canManage ? 'Activity log and admin tools' : 'Your activity in this organization'}
      </summary>
      {open ? (
        <div className="mt-4 space-y-6">
          <div className="space-y-2">
            <h3 className="text-xs font-medium uppercase tracking-wide text-muted">Activity log</h3>
            {audit.isLoading ? <p className="text-xs text-muted">Loading…</p> : null}
            {audit.isError ? <p className="text-xs text-danger">{errorText(audit.error)}</p> : null}
            <ul className="space-y-1">
              {(audit.data?.items ?? []).map((ev, i) => (
                <li key={`${ev.at}-${ev.action}-${i}`} className="text-xs text-fg-dim">
                  <span className="text-fg">{ev.action.replaceAll('.', ' ')}</span>
                  {ev.actor_user_id ? ` · ${nameFor(ev.actor_user_id)}` : ''} · {formatRelative(ev.at)}
                </li>
              ))}
              {audit.isSuccess && (audit.data?.items ?? []).length === 0 ? <li className="text-xs text-muted">Nothing yet.</li> : null}
            </ul>
          </div>

          {canManage ? (
            <div className="space-y-2">
              <h3 className="text-xs font-medium uppercase tracking-wide text-muted">Saved chats per person</h3>
              <p className="text-[11px] text-muted">Counts only. Chat titles stay private to their owner.</p>
              <ul className="space-y-1 text-sm text-fg">
                {(storage.data?.members ?? []).map((row) => (
                  <li key={row.user_id} className="flex justify-between gap-3">
                    <span className="truncate text-xs" title={row.user_id}>{nameFor(row.user_id)}</span>
                    <span className="text-xs text-fg-dim">
                      {row.session_count} chats
                      {row.last_active_at ? ` · ${formatRelative(row.last_active_at)}` : ''}
                    </span>
                  </li>
                ))}
                {storage.isSuccess && (storage.data?.members ?? []).length === 0 ? (
                  <li className="text-xs text-muted">No saved chats yet.</li>
                ) : null}
              </ul>
            </div>
          ) : null}

          {canManage ? (
            <div className="space-y-2">
              <h3 className="text-xs font-medium uppercase tracking-wide text-muted">Shared chats</h3>
              <ul className="divide-y divide-border">
                {(shares.data?.items ?? []).map((row) => (
                  <li key={`${row.session_id}-${row.grantee_id}`} className="flex flex-wrap items-center gap-3 py-2">
                    <span className="min-w-0 flex-1 truncate text-xs text-fg-dim">
                      {nameFor(row.owner_id)} → {nameFor(row.grantee_id)}
                      {row.live ? ' · live' : ''}
                    </span>
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      disabled={revokeShare.isPending}
                      onClick={() =>
                        revokeShare.mutate(
                          { sessionId: row.session_id, userId: row.grantee_id },
                          {
                            onSuccess: () => push({ title: 'Share removed', tone: 'teal' }),
                            onError: (err) => push({ title: 'Couldn’t remove share', detail: errorText(err), tone: 'danger' }),
                          },
                        )
                      }
                    >
                      Stop sharing
                    </Button>
                  </li>
                ))}
                {shares.isSuccess && (shares.data?.items ?? []).length === 0 ? (
                  <li className="py-1 text-xs text-muted">No shared chats.</li>
                ) : null}
              </ul>
            </div>
          ) : null}

          {canManage ? (
            <div className="space-y-2">
              <h3 className="text-xs font-medium uppercase tracking-wide text-muted">Someone is leaving</h3>
              <p className="text-[11px] text-muted">
                Moves their organization chats to another person and signs out their tokens. Preview first.
              </p>
              <form
                onSubmit={(e) => {
                  e.preventDefault()
                  const from = offboardUser.trim()
                  if (from) previewOffboard.mutate(from, { onError: (err) => push({ title: 'Preview failed', detail: errorText(err), tone: 'danger' }) })
                }}
                className="flex flex-wrap items-end gap-2"
              >
                <label className="block min-w-0 flex-1">
                  <span className="mb-1 block text-xs text-fg-dim">Leaving (user ID)</span>
                  <input
                    value={offboardUser}
                    onChange={(e) => {
                      setOffboardUser(e.target.value)
                      setPreview(null)
                    }}
                    className={`${inputClass} font-mono`}
                  />
                </label>
                <label className="block min-w-0 flex-1">
                  <span className="mb-1 block text-xs text-fg-dim">Takes over (user ID)</span>
                  <input value={offboardReceiver} onChange={(e) => setOffboardReceiver(e.target.value)} className={`${inputClass} font-mono`} />
                </label>
                <Button type="submit" size="sm" variant="secondary" disabled={previewOffboard.isPending}>
                  Preview
                </Button>
              </form>
              {preview ? (
                <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-raised/40 px-3 py-2.5">
                  <p className="flex-1 text-xs text-fg">
                    {preview.sessions} chats · {preview.grants} shares · {preview.tokens} tokens
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
                    onClick={() => {
                      if (!window.confirm('Move these chats and sign out the leaving person’s tokens?')) return
                      runOffboard.mutate(
                        { from_user_id: offboardUser.trim(), to_user_id: offboardReceiver.trim() },
                        {
                          onSuccess: (res) => push({ title: 'Done', detail: `${res.sessions_transferred} chats moved`, tone: 'teal' }),
                          onError: (err) => push({ title: 'Couldn’t finish', detail: errorText(err), tone: 'danger' }),
                        },
                      )
                    }}
                  >
                    Confirm
                  </Button>
                </div>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}
    </details>
  )
}

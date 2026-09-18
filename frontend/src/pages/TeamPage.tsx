import { motion } from 'framer-motion'
import { FolderGit2, UserPlus } from 'lucide-react'
import { useMemo, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import {
  useActiveWorkspaces,
  useGitHubConnect,
  useGitHubDisconnect,
  useGitHubImport,
  useGitHubStatus,
  useGrantMember,
  useMembers,
  useRevokeMember,
  useRoles,
  useSetMemberRole,
} from '@/hooks/useTeam'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import type { GitHubImportItem } from '@/api/team'
import { formatRelative } from '@/utils/format'

const BUILTIN_ROLES = ['OWNER', 'ADMIN', 'EDITOR', 'VIEWER'] as const

function initials(id: string) {
  const s = id.replace(/^user_/, '').slice(0, 2)
  return (s || '??').toUpperCase()
}

export function TeamPage() {
  const { projectId, user } = useAuth()
  const { push } = useToast()
  const members = useMembers()
  const roles = useRoles()
  const github = useGitHubStatus()
  const presence = useActiveWorkspaces()
  const grant = useGrantMember()
  const setRole = useSetMemberRole()
  const revoke = useRevokeMember()
  const connect = useGitHubConnect()
  const disconnect = useGitHubDisconnect()
  const doImport = useGitHubImport()

  const [inviteId, setInviteId] = useState('')
  const [owner, setOwner] = useState('')
  const [repo, setRepo] = useState('')
  const [token, setToken] = useState('')
  const [importItems, setImportItems] = useState<GitHubImportItem[] | null>(null)

  const onlineByUser = useMemo(() => {
    const map = new Set<string>()
    for (const ws of presence.data ?? []) {
      const uid = (ws as { user_id?: string }).user_id
      if (uid) map.add(uid)
    }
    return map
  }, [presence.data])

  const roleOptions = useMemo(() => {
    const names = new Set<string>(BUILTIN_ROLES)
    for (const r of roles.data ?? []) {
      if (r.name) names.add(r.name)
    }
    return [...names]
  }, [roles.data])

  const onInvite = (e: FormEvent) => {
    e.preventDefault()
    const id = inviteId.trim()
    if (!id) return
    grant.mutate(id, {
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
    })
  }

  const onConnect = (e: FormEvent) => {
    e.preventDefault()
    connect.mutate(
      {
        owner: owner.trim(),
        repo: repo.trim(),
        access_token: token.trim() || undefined,
        sync_mode: 'manual',
      },
      {
        onSuccess: () => {
          push({ title: 'GitHub connected', detail: `${owner}/${repo}` })
          setToken('')
        },
        onError: (err) =>
          push({
            title: 'Connect failed',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  const onImport = () => {
    doImport.mutate(undefined, {
      onSuccess: (res) => {
        setImportItems(res.items)
        push({
          title: 'Import complete',
          detail: `${res.imported} granted · ${res.invites} need signup`,
          tone: 'teal',
        })
      },
      onError: (err) =>
        push({
          title: 'Import failed',
          detail: err instanceof ApiError ? err.message : 'Unknown error',
          tone: 'danger',
        }),
    })
  }

  if (!projectId) {
    return (
      <div className="py-16 text-sm text-muted">Resolve a project first (sign in again if needed).</div>
    )
  }

  return (
    <div className="space-y-8">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-fg">Team</h1>
          <p className="mt-1 text-sm text-fg-dim">
            Members, roles, presence, and GitHub collaborator import.
          </p>
        </div>
        <div className="flex -space-x-2">
          {(members.data ?? []).slice(0, 8).map((m, i) => (
            <motion.div
              key={m.user_id}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ delay: i * 0.04 }}
              title={m.user_id}
              className="app-avatar relative flex h-9 w-9 items-center justify-center rounded-full border border-border bg-raised text-[10px] font-medium text-fg"
            >
              {initials(m.user_id)}
              <span
                className={[
                  'absolute bottom-0 right-0 h-2 w-2 rounded-full border border-surface',
                  onlineByUser.has(m.user_id) ? 'bg-teal' : 'bg-muted',
                ].join(' ')}
              />
            </motion.div>
          ))}
        </div>
      </div>

      <GlassPanel className="space-y-4 p-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-medium text-fg">Members</h2>
          <StatusPill tone="accent">{`${(members.data ?? []).length} people`}</StatusPill>
        </div>

        <form onSubmit={onInvite} className="flex flex-wrap items-end gap-2">
          <label className="block min-w-[14rem] flex-1">
            <span className="mb-1 block text-xs text-fg-dim">Invite by user id</span>
            <input
              value={inviteId}
              onChange={(e) => setInviteId(e.target.value)}
              placeholder="user uuid or username id"
              className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
            />
          </label>
          <Button type="submit" size="sm" disabled={grant.isPending}>
            <UserPlus className="h-3.5 w-3.5" />
            Add
          </Button>
        </form>

        <ul className="divide-y divide-border">
          {(members.data ?? []).map((m) => {
            const isSelf = m.user_id === user?.userId
            const isOwner = m.role === 'OWNER'
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
                  <p className="text-xs text-fg-dim">
                    {onlineByUser.has(m.user_id) ? 'Online' : 'Away'}
                  </p>
                </div>
                <select
                  value={m.role ?? 'EDITOR'}
                  disabled={isOwner || setRole.isPending}
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
                  className="h-9 rounded-lg border border-border bg-raised px-2 text-xs text-fg outline-none focus:border-amber"
                >
                  {roleOptions.map((r) => (
                    <option key={r} value={r}>
                      {r}
                    </option>
                  ))}
                </select>
                {!isOwner && !isSelf ? (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    disabled={revoke.isPending}
                    onClick={() =>
                      revoke.mutate(m.user_id, {
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
          {members.isLoading ? (
            <li className="py-6 text-sm text-muted">Loading members…</li>
          ) : null}
        </ul>
      </GlassPanel>

      <GlassPanel className="space-y-4 p-5">
        <div className="flex flex-wrap items-center gap-2">
          <FolderGit2 className="h-4 w-4 text-fg-dim" />
          <h2 className="text-sm font-medium text-fg">GitHub import</h2>
          {github.data?.connected ? (
            <StatusPill tone="teal">{`${github.data.owner ?? ''}/${github.data.repo ?? ''}`}</StatusPill>
          ) : (
            <StatusPill>not linked</StatusPill>
          )}
        </div>

        {github.data?.connected ? (
          <div className="flex flex-wrap gap-2">
            <Button type="button" size="sm" onClick={onImport} disabled={doImport.isPending}>
              {doImport.isPending ? 'Importing…' : 'Import collaborators'}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="secondary"
              disabled={disconnect.isPending}
              onClick={() =>
                disconnect.mutate(undefined, {
                  onSuccess: () => {
                    setImportItems(null)
                    push({ title: 'GitHub disconnected' })
                  },
                })
              }
            >
              Disconnect
            </Button>
            {github.data.last_import_at ? (
              <span className="self-center text-xs text-muted">
                Last import {formatRelative(github.data.last_import_at)}
              </span>
            ) : null}
          </div>
        ) : (
          <form onSubmit={onConnect} className="grid gap-3 sm:grid-cols-2">
            <label className="block">
              <span className="mb-1 block text-xs text-fg-dim">Owner</span>
              <input
                value={owner}
                onChange={(e) => setOwner(e.target.value)}
                required
                className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
              />
            </label>
            <label className="block">
              <span className="mb-1 block text-xs text-fg-dim">Repo</span>
              <input
                value={repo}
                onChange={(e) => setRepo(e.target.value)}
                required
                className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
              />
            </label>
            <label className="block sm:col-span-2">
              <span className="mb-1 block text-xs text-fg-dim">
                Access token (optional if server has GITHUB_TOKEN)
              </span>
              <input
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                className="h-10 w-full rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none focus:border-amber"
              />
            </label>
            <div>
              <Button type="submit" size="sm" disabled={connect.isPending}>
                Connect repository
              </Button>
            </div>
          </form>
        )}

        {importItems ? (
          <ul className="max-h-64 space-y-2 overflow-y-auto border-t border-border pt-3">
            {importItems.map((item) => (
              <li
                key={item.login}
                className="flex flex-wrap items-center justify-between gap-2 text-sm"
              >
                <span className="font-mono text-fg">@{item.login}</span>
                <span className="text-xs text-fg-dim">
                  {item.github_permission} → {item.mapped_role}
                </span>
                <StatusPill
                  tone={
                    item.status === 'invite'
                      ? 'amber'
                      : item.status === 'skipped'
                        ? 'neutral'
                        : 'teal'
                  }
                >
                  {item.status}
                </StatusPill>
              </li>
            ))}
          </ul>
        ) : null}
      </GlassPanel>
    </div>
  )
}

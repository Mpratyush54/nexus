import { motion } from 'framer-motion'
import { ExternalLink, FolderGit2, UserPlus } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Advanced } from '@/components/Advanced'
import { TeamTabs } from '@/components/TeamTabs'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import {
  useGitHubConnect,
  useGitHubDisconnect,
  useGitHubImport,
  useGitHubOAuthStart,
  useGitHubRepos,
  useGitHubStatus,
  useGrantMember,
  useMembers,
  useProjectPresence,
  useRevokeMember,
  useRoles,
  useSetMemberRole,
} from '@/hooks/useTeam'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import type { GitHubImportItem } from '@/api/team'
import { formatRelative } from '@/utils/format'
import { initialsOf, usePeople } from '@/hooks/usePeople'
import { MEMBER_ID_HELP, memberIdProblem, normalizeMemberId } from '@/lib/member-id'

// Roles someone can be given here. Owner is never offered: ownership moves
// on the Organization page, and nobody changes their own role.
const ASSIGNABLE_ROLES = ['ADMIN', 'EDITOR', 'VIEWER'] as const
const ROLE_HINT: Record<string, string> = {
  admin: 'Can add and remove people',
  editor: 'Can edit memory',
  member: 'Can edit memory',
  viewer: 'Can read chats and memory',
}

/** "OWNER" / "owner" → "Owner". */
function roleLabel(role: string) {
  const r = role.trim().toLowerCase()
  return r ? r[0].toUpperCase() + r.slice(1) : r
}


export function TeamPage() {
  const { projectId, user } = useAuth()
  const { push } = useToast()
  const [params, setParams] = useSearchParams()
  const members = useMembers()
  const roles = useRoles()
  const github = useGitHubStatus()
  const presence = useProjectPresence()
  const { nameFor } = usePeople()
  const grant = useGrantMember()
  const setRole = useSetMemberRole()
  const revoke = useRevokeMember()
  const connect = useGitHubConnect()
  const disconnect = useGitHubDisconnect()
  const doImport = useGitHubImport()
  const oauthStart = useGitHubOAuthStart()

  const [inviteId, setInviteId] = useState('')
  const [showRepos, setShowRepos] = useState(false)
  const [importItems, setImportItems] = useState<GitHubImportItem[] | null>(null)

  const suggestedOwner = github.data?.suggested_owner?.trim() || ''
  const suggestedRepo = github.data?.suggested_repo?.trim() || ''
  const connected = Boolean(github.data?.connected)
  const oauthLinked = Boolean(github.data?.oauth_linked)
  const oauthConfigured = Boolean(github.data?.oauth_configured)
  const repos = useGitHubRepos(showRepos && !connected && (oauthLinked || oauthConfigured))

  useEffect(() => {
    const flag = params.get('github')
    if (!flag) return
    if (flag === 'linked') {
      setShowRepos(true)
      push({ title: 'GitHub connected', detail: 'Pick a repository to link', tone: 'teal' })
    } else if (flag === 'error') {
      push({
        title: 'GitHub connect failed',
        detail: params.get('message') || 'OAuth error',
        tone: 'danger',
      })
    }
    setParams({}, { replace: true })
  }, [params, push, setParams])

  useEffect(() => {
    if (oauthLinked && !connected) setShowRepos(true)
  }, [oauthLinked, connected])

  const onlineByUser = useMemo(() => {
    const map = new Set<string>()
    for (const p of presence.data ?? []) {
      if (p.user_id && p.status !== 'offline') map.add(p.user_id)
    }
    if (user?.userId) map.add(user.userId)
    return map
  }, [presence.data, user?.userId])

  // One option per role, whatever case the server uses; the server's own
  // spelling wins so the value we send back is one it knows.
  const roleOptions = useMemo(() => {
    const byKey = new Map<string, string>()
    // The project's own roles when the server lists them; the built-ins otherwise.
    if (!roles.data?.length) for (const r of ASSIGNABLE_ROLES) byKey.set(r.toLowerCase(), r)
    for (const r of roles.data ?? []) {
      const key = r.name?.trim().toLowerCase()
      if (key && key !== 'owner') byKey.set(key, r.name)
    }
    return [...byKey.values()]
  }, [roles.data])

  const onInvite = (e: FormEvent) => {
    e.preventDefault()
    const id = normalizeMemberId(inviteId)
    if (!id) return
    const problem = memberIdProblem(id)
    if (problem) {
      push({ ...problem, tone: 'amber' })
      return
    }
    grant.mutate(id, {
      onSuccess: () => {
        push({ title: 'Member added', detail: nameFor(id), tone: 'teal' })
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

  const runImport = () => {
    doImport.mutate(undefined, {
      onSuccess: (res) => {
        setImportItems(res.items)
        setShowRepos(false)
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

  const linkAndImport = (owner: string, repo: string) => {
    connect.mutate(
      { owner, repo, sync_mode: 'one_time' },
      {
        onSuccess: () => runImport(),
        onError: (err) =>
          push({
            title: 'Connect failed',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  const onPrimaryGitHub = () => {
    if (connected) {
      runImport()
      return
    }
    if (suggestedOwner && suggestedRepo) {
      linkAndImport(suggestedOwner, suggestedRepo)
      return
    }
    if (oauthLinked) {
      setShowRepos(true)
      return
    }
    if (oauthConfigured) {
      oauthStart.mutate(undefined, {
        onSuccess: (res) => {
          window.location.href = res.authorize_url
        },
        onError: (err) =>
          push({
            title: 'Could not start GitHub',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      })
      return
    }
    push({
      title: 'GitHub OAuth not configured',
      detail: 'Set GITHUB_CLIENT_ID / SECRET on the server, or resolve a project from a GitHub remote.',
      tone: 'amber',
    })
  }

  const importBusy = connect.isPending || doImport.isPending || oauthStart.isPending
  const repoURL =
    github.data?.html_url ||
    (github.data?.owner && github.data?.repo
      ? `https://github.com/${github.data.owner}/${github.data.repo}`
      : '')

  if (!projectId) {
    return (
      <div className="space-y-6">
        <TeamTabs />
        <p className="py-10 text-sm text-muted">
          Pick a project in the sidebar to see who can open it. Organizations are on the Organization tab.
        </p>
      </div>
    )
  }

  return (
    <div className="space-y-8">
      <TeamTabs />
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-fg">Team</h1>
          <p className="mt-1 text-sm text-fg-dim">
            People who can see this project’s chats and memory.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Link
            to="/app/org#invite"
            className="inline-flex h-9 items-center gap-1.5 rounded-lg bg-amber px-3 text-sm font-medium text-ink hover:opacity-90"
          >
            <UserPlus className="h-3.5 w-3.5" />
            Invite by email
          </Link>
          <div className="flex -space-x-2">
            {(members.data ?? []).slice(0, 8).map((m, i) => (
              <motion.div
                key={m.user_id}
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ delay: i * 0.04 }}
                title={`${nameFor(m.user_id, { you: false })} · ${onlineByUser.has(m.user_id) ? 'online' : 'away'}`}
                className="app-avatar relative flex h-9 w-9 items-center justify-center rounded-full border border-border bg-raised text-[10px] font-medium text-fg"
              >
                {initialsOf(nameFor(m.user_id, { you: false }))}
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
      </div>

      <GlassPanel className="space-y-4 p-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-medium text-fg">Members</h2>
          <StatusPill tone="accent">{`${(members.data ?? []).length} people`}</StatusPill>
        </div>


        <ul className="divide-y divide-border">
          {(members.data ?? []).map((m) => {
            const isSelf = m.user_id === user?.userId
            const isOwner = (m.role ?? '').toLowerCase() === 'owner'
            return (
              <li key={m.user_id} className="flex flex-wrap items-center gap-3 py-3">
                <div className="app-avatar flex h-9 w-9 items-center justify-center rounded-full bg-raised text-[10px] font-medium" aria-hidden>
                  {initialsOf(nameFor(m.user_id, { you: false }))}
                </div>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm text-fg" title={m.user_id}>
                    {nameFor(m.user_id, { you: false })}
                    {isSelf ? <span className="ml-2 text-xs text-muted">(you)</span> : null}
                  </p>
                  <p className="text-xs text-fg-dim">
                    {onlineByUser.has(m.user_id) ? 'Online' : 'Away'}
                  </p>
                </div>
                {isOwner || isSelf ? (
                  <span
                    className="inline-flex h-9 items-center rounded-lg px-2 text-xs text-fg-dim"
                    title={isOwner ? 'Owners have full control' : 'You can’t change your own role'}
                  >
                    {roleLabel(m.role ?? 'EDITOR')}
                  </span>
                ) : (
                  <select
                    aria-label={`Role for ${nameFor(m.user_id, { you: false })}`}
                    value={roleOptions.find((r) => r.toLowerCase() === (m.role ?? 'editor').toLowerCase()) ?? m.role ?? 'EDITOR'}
                    disabled={setRole.isPending}
                    onChange={(e) =>
                      setRole.mutate(
                        { userId: m.user_id, role: e.target.value },
                        {
                          onSuccess: () => push({ title: 'Role updated', detail: roleLabel(e.target.value) }),
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
                      <option key={r} value={r} title={ROLE_HINT[r.toLowerCase()]}>
                        {roleLabel(r)}
                      </option>
                    ))}
                  </select>
                )}
                {!isOwner && !isSelf ? (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    disabled={revoke.isPending}
                    onClick={() =>
                      revoke.mutate(m.user_id, {
                        onSuccess: () => push({ title: 'Member removed', detail: nameFor(m.user_id, { you: false }) }),
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
                    <span className="sr-only"> {nameFor(m.user_id, { you: false })}</span>
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

      <Advanced title="Other ways to add people" summary="Import collaborators from GitHub, or add someone by their Nexus user ID.">
        <div className="space-y-5">
          <GlassPanel className="space-y-4 p-5">
            <div className="flex flex-wrap items-center gap-2">
              <FolderGit2 className="h-4 w-4 text-fg-dim" />
              <h2 className="text-sm font-medium text-fg">GitHub</h2>
              {connected ? (
                <StatusPill tone="teal">{`${github.data?.owner ?? ''}/${github.data?.repo ?? ''}`}</StatusPill>
              ) : suggestedOwner && suggestedRepo ? (
                <StatusPill tone="accent">{`${suggestedOwner}/${suggestedRepo}`}</StatusPill>
              ) : oauthLinked ? (
                <StatusPill tone="teal">account linked</StatusPill>
              ) : (
                <StatusPill>not linked</StatusPill>
              )}
            </div>
            <p className="text-xs text-fg-dim">
              Connect with GitHub, pick a repo, import collaborators. No typing owner or token.
            </p>

            <div className="flex flex-wrap items-center gap-2">
              <Button type="button" size="sm" onClick={onPrimaryGitHub} disabled={importBusy}>
                {importBusy
                  ? 'Working…'
                  : connected
                    ? 'Import collaborators'
                    : suggestedOwner && suggestedRepo
                      ? 'Import from GitHub'
                      : oauthLinked
                        ? 'Choose repository'
                        : 'Connect GitHub'}
              </Button>
              {connected && repoURL ? (
                <a
                  href={repoURL}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border px-3 text-xs text-fg-dim hover:border-border-strong hover:text-fg"
                >
                  <ExternalLink className="h-3.5 w-3.5" />
                  Open repo
                </a>
              ) : null}
              {connected ? (
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
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
              ) : null}
              {github.data?.last_import_at ? (
                <span className="self-center text-xs text-muted">
                  Last import {formatRelative(github.data.last_import_at)}
                </span>
              ) : null}
            </div>

            {showRepos && !connected ? (
              <div className="max-h-72 space-y-1 overflow-y-auto border-t border-border pt-3">
                {repos.isLoading ? <p className="text-sm text-muted">Loading your repositories…</p> : null}
                {repos.isError ? (
                  <p className="text-sm text-danger">
                    {repos.error instanceof ApiError ? repos.error.message : 'Could not list repos'}
                  </p>
                ) : null}
                {(repos.data ?? []).map((r) => (
                  <button
                    key={r.id}
                    type="button"
                    disabled={importBusy}
                    onClick={() => linkAndImport(r.owner, r.name)}
                    className="flex w-full items-center justify-between gap-3 rounded-lg px-3 py-2 text-left text-sm hover:bg-raised"
                  >
                    <span className="min-w-0">
                      <span className="block truncate font-mono text-fg">{r.full_name}</span>
                      {r.description ? (
                        <span className="block truncate text-xs text-muted">{r.description}</span>
                      ) : null}
                    </span>
                    <StatusPill tone={r.private ? 'amber' : 'neutral'}>{r.private ? 'private' : 'public'}</StatusPill>
                  </button>
                ))}
                {!repos.isLoading && !(repos.data ?? []).length && oauthLinked ? (
                  <p className="text-sm text-muted">No repositories returned for this GitHub account.</p>
                ) : null}
              </div>
            ) : null}

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
            <form onSubmit={onInvite} className="flex flex-wrap items-end gap-2">
              <label className="block min-w-[14rem] flex-1">
                <span className="mb-1 block text-xs text-fg-dim">Add a teammate by user ID</span>
                <input
                  value={inviteId}
                  onChange={(e) => setInviteId(e.target.value)}
                  placeholder="e.g. 1b2c3d4e-5f60-…"
                  aria-describedby="invite-help"
                  autoComplete="off"
                  spellCheck={false}
                  className="h-10 w-full rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none focus:border-amber"
                />
                <span id="invite-help" className="mt-1 block text-xs text-muted">
                  Paste a user ID, not a username or email: Nexus can’t look people up yet. {MEMBER_ID_HELP}{' '}
                  To invite by email, use your organization page.
                </span>
              </label>
              <Button type="submit" size="sm" disabled={grant.isPending}>
                <UserPlus className="h-3.5 w-3.5" />
                {grant.isPending ? 'Adding…' : 'Add member'}
              </Button>
            </form>
        </div>
      </Advanced>
    </div>
  )
}

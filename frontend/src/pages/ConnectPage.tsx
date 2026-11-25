import { motion } from 'framer-motion'
import {
  Check,
  ExternalLink,
  Plug,
  RefreshCw,
  ScanSearch,
} from 'lucide-react'
import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { Advanced } from '@/components/Advanced'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import {
  useBindLocalWorkspace,
  useLocalGitStatus,
  useLocalHarvest,
  useTriggerHarvestScan,
} from '@/hooks/useDaemon'
import { useFollowHarvestProject } from '@/hooks/useFollowHarvestProject'
import { useTrustedLocalBridge } from '@/hooks/useTrustedLocalBridge'
import { useMemorySearch } from '@/hooks/useMemory'
import { useAuth } from '@/providers/AuthProvider'
import { DesktopInstallPicker } from '@/components/DesktopInstallPicker'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'
import { daemonApi } from '@/api/daemon'
import { projectLabel } from '@/lib/project-label'

function folderFromPath(path?: string) {
  if (!path) return ''
  const parts = path.replace(/[/\\]+$/, '').split(/[/\\]/)
  return parts[parts.length - 1] || ''
}

function shortTime(iso?: string) {
  if (!iso) return '—'
  try {
    return formatRelative(iso)
  } catch {
    return iso
  }
}

export function ConnectPage() {
  const { projectId, user } = useAuth()
  const { push } = useToast()
  const bridge = useTrustedLocalBridge()
  const bind = useBindLocalWorkspace()

  const bridgeUrl = bridge.bridgeUrl
  const harvest = useLocalHarvest(bridgeUrl)
  const git = useLocalGitStatus(bridgeUrl)
  const scanNow = useTriggerHarvestScan(bridgeUrl)
  const harvestedMemories = useMemorySearch('')
  useFollowHarvestProject(harvest.data, bridge.trusted)

  const detected = bridge.local.data
  const folder = folderFromPath(detected?.status.root || harvest.data?.root)
  const harvestOnline = bridge.trusted && (Boolean(detected) || Boolean(bridge.serverView.data?.online))
  const hs = harvest.data
  const activeAgents = (hs?.agents ?? []).filter((a) => a.active).length

  const steps = useMemo(() => {
    return [
      {
        id: 'desktop',
        label: 'Install the app',
        done: Boolean(detected) || Boolean(bridge.serverView.data?.online),
        hint:
          Boolean(detected) || bridge.serverView.data?.online
            ? 'Found on this computer'
            : 'Download and run the installer below',
      },
      {
        id: 'same-account',
        label: 'Sign in',
        done: bridge.identity === 'match',
        hint:
          bridge.identity === 'match'
            ? `Linked as ${user?.username ?? 'you'}`
            : bridge.identity === 'mismatch'
              ? 'The app is signed in as someone else — sign in again'
              : 'Use the same account as this page',
      },
      {
        id: 'scan',
        label: 'Chats are saved',
        done: Boolean(hs?.running && hs.designated),
        hint: hs?.running
          ? 'Your chats are being saved'
          : 'Starts once the app is running',
      },
    ]
  }, [detected, bridge.serverView.data?.online, bridge.identity, hs, user?.username])

  const onBindFolder = () => {
    if (!bridge.trusted) {
      push({
        title: 'Accounts must match',
        detail: 'Sign the desktop app in with this account first',
        tone: 'danger',
      })
      return
    }
    if (!folder) {
      push({ title: 'No folder found', detail: 'Open Nexus on this computer first', tone: 'danger' })
      return
    }
    bind.mutate(folder, {
      onSuccess: (p) =>
        push({ title: 'Workspace linked', detail: projectLabel(p), tone: 'teal' }),
      onError: (err) =>
        push({
          title: 'Link failed',
          detail: err instanceof ApiError ? err.message : String(err),
          tone: 'danger',
        }),
    })
  }

  const onScanNow = () => {
    if (!bridge.trusted) {
      push({
        title: 'Blocked',
        detail: 'The desktop app uses a different account',
        tone: 'danger',
      })
      return
    }
    scanNow.mutate(undefined, {
      onSuccess: (st) =>
        push({
          title: 'Check complete',
          detail: st.message || `${st.last_scan_files ?? 0} files · ${st.last_scan_turns ?? 0} messages`,
          tone: 'teal',
        }),
      onError: (err) =>
        push({
          title: 'Check failed',
          detail: err instanceof Error ? err.message : 'The desktop app didn’t answer',
          tone: 'danger',
        }),
    })
  }

  const onRelink = () => {
    const base = detected?.baseUrl
    if (!base) {
      push({ title: 'Desktop app not running', detail: 'Install or open Nexus first', tone: 'danger' })
      return
    }
    void daemonApi
      .startBrowserLogin(base)
      .then(() =>
        push({
          title: 'Sign-in opened',
          detail: 'Use the same account as this page',
          tone: 'teal',
        }),
      )
      .catch((err) =>
        push({
          title: 'Login failed',
          detail: err instanceof Error ? err.message : String(err),
          tone: 'danger',
        }),
      )
  }

  const gitBranch = git.data?.branch
  const gitDirty = Boolean(git.data?.dirty ?? git.data?.is_dirty)

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Desktop app</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-dim">
          Install the desktop app on the computer where you code. It saves your AI coding chats so you
          and your team can find them here. Sign in with this account (
          <span className="text-fg">{user?.username ?? 'you'}</span>).
        </p>
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        {steps.map((s, i) => (
          <motion.div
            key={s.id}
            initial={{ opacity: 0, y: 8 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ delay: i * 0.04 }}
          >
            <GlassPanel className="flex items-start gap-3 p-4" glow={s.done ? 'amber' : 'none'}>
              <span
                className={[
                  'mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-xs font-semibold',
                  s.done ? 'bg-teal/20 text-teal' : 'bg-raised text-muted',
                ].join(' ')}
              >
                {s.done ? <Check size={14} /> : i + 1}
              </span>
              <div className="min-w-0">
                <p className="text-sm font-medium text-fg">{s.label}</p>
                <p className="mt-0.5 text-xs text-fg-dim">{s.hint}</p>
              </div>
            </GlassPanel>
          </motion.div>
        ))}
      </div>

      <GlassPanel className="space-y-4 p-5">
        <DesktopInstallPicker />
        <div className="border-t border-border pt-4">
          <p className="text-xs text-fg-dim">Already installed? Make sure the app uses this account.</p>
          <Button type="button" size="sm" variant="secondary" className="mt-2" onClick={onRelink}>
            Sign in the desktop app
          </Button>
        </div>
      </GlassPanel>

      <Advanced title="Check this computer" summary="See whether the desktop app here is running, which folder it watches, and scan now.">
          {/* Live harvest — only when trusted */}
          <GlassPanel className="space-y-5 p-5" glow={hs?.running ? 'amber' : 'none'}>
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="flex items-center gap-2">
                <ScanSearch size={18} className="text-amber" />
                <div>
                  <h2 className="text-sm font-medium text-fg">Chat saving on this computer</h2>
                  <p className="mt-0.5 text-xs text-fg-dim">
                    {!bridge.trusted
                      ? 'Shows once the desktop app is signed in with this account'
                      : hs?.message ||
                        (harvestOnline
                          ? 'Reading your agent chats on this computer…'
                          : 'Open Nexus on this computer to see its progress here')}
                  </p>
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <StatusPill tone={hs?.running ? 'teal' : harvestOnline ? 'amber' : 'danger'}>
                  {hs?.running ? 'saving chats' : harvestOnline ? 'open, not saving' : 'not running'}
                </StatusPill>
                {bridge.trusted ? (
                  <StatusPill tone={hs?.designated ? 'teal' : 'neutral'}>
                    {hs?.designated ? 'saves chats' : 'another computer saves chats'}
                  </StatusPill>
                ) : null}
              </div>
            </div>

            {bridge.trusted ? (
              <>
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                  {[
                    { label: 'Last scan', value: shortTime(hs?.last_scan_at) },
                    {
                      label: 'Files · new messages',
                      value: `${hs?.last_scan_files ?? 0} · ${hs?.last_scan_turns ?? 0}`,
                    },
                    { label: 'Messages saved', value: String(hs?.turns_emitted ?? 0) },
                    { label: 'Agents found', value: String(activeAgents) },
                  ].map((c) => (
                    <div key={c.label} className="rounded-lg border border-border bg-raised/40 px-3 py-2.5">
                      <p className="text-[11px] uppercase tracking-wide text-muted">{c.label}</p>
                      <p className="mt-1 font-mono text-sm text-fg">{c.value}</p>
                    </div>
                  ))}
                </div>

                {(detected || hs) && (
                  <div className="grid gap-2 rounded-lg border border-border bg-raised/50 px-3 py-2.5 text-xs text-fg-dim sm:grid-cols-2">
                    <p>
                      <span className="text-muted">Folder</span>{' '}
                      <span className="font-mono text-fg">{hs?.root || detected?.status.root || '—'}</span>
                    </p>
                    <p>
                      <span className="text-muted">Project</span>{' '}
                      <span className="font-mono text-fg">
                        {hs?.project_id?.slice(0, 8) || projectId?.slice(0, 8) || '—'}
                        {(hs?.project_id || projectId) && '…'}
                      </span>
                    </p>
                    <p>
                      <span className="text-muted">Git</span>{' '}
                      {gitBranch ? (
                        <>
                          <span className="font-mono text-fg">{gitBranch}</span>
                          {gitDirty ? ' · dirty' : ' · clean'}
                        </>
                      ) : (
                        '—'
                      )}
                    </p>
                    <p>
                      <span className="text-muted">Checks</span> every {hs?.poll_seconds ?? 30}s
                    </p>
                  </div>
                )}

                <div className="flex flex-wrap gap-2">
                  <Button
                    type="button"
                    size="sm"
                    onClick={onScanNow}
                    disabled={!bridgeUrl || scanNow.isPending}
                  >
                    <ScanSearch size={14} className={scanNow.isPending ? 'animate-pulse' : ''} />
                    {scanNow.isPending ? 'Checking…' : 'Check for new chats'}
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="secondary"
                    onClick={() => {
                      void bridge.local.refetch()
                      void harvest.refetch()
                      void git.refetch()
                    }}
                    disabled={bridge.local.isFetching || harvest.isFetching}
                  >
                    <RefreshCw
                      size={14}
                      className={bridge.local.isFetching || harvest.isFetching ? 'animate-spin' : ''}
                    />
                    Refresh
                  </Button>
                  {detected?.baseUrl ? (
                    <a
                      className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border px-3 text-xs text-fg-dim hover:border-border-strong hover:text-fg"
                      href={detected.baseUrl}
                      target="_blank"
                      rel="noreferrer"
                      title="Opens the desktop app's own status page. It only works on the computer running the app."
                    >
                      Status page <ExternalLink size={12} aria-hidden />
                    </a>
                  ) : null}
                  {folder ? (
                    <Button
                      type="button"
                      size="sm"
                      variant="secondary"
                      onClick={onBindFolder}
                      disabled={bind.isPending}
                    >
                      <Plug size={14} />
                      Use this folder as project
                    </Button>
                  ) : null}
                </div>

                {hs?.last_scan_error || hs?.last_proposal_error ? (
                  <p className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
                    {hs.last_proposal_error || hs.last_scan_error}
                  </p>
                ) : null}

                <div>
                  <p className="mb-2 text-xs font-medium text-fg">Agents</p>
                  <div className="flex flex-wrap gap-1.5">
                    {(hs?.agents?.length ? hs.agents : []).map((a) => (
                      <span
                        key={`${a.name}-${a.format}`}
                        className={[
                          'inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-[11px]',
                          a.active
                            ? 'border-teal/40 bg-teal/10 text-fg'
                            : 'border-border bg-raised/40 text-muted',
                        ].join(' ')}
                      >
                        <span className={a.active ? 'text-teal' : ''}>{a.name}</span>
                        <span className="font-mono text-muted">{a.format}</span>
                        <span className="font-mono">{a.files_seen}</span>
                      </span>
                    ))}
                    {!hs?.agents?.length ? (
                      <span className="text-xs text-muted">Waiting for the list of agents…</span>
                    ) : null}
                  </div>
                </div>

                <div className="grid gap-4 lg:grid-cols-2">
                  <div>
                    <p className="mb-2 text-xs font-medium text-fg">Recent activity</p>
                    <ul className="max-h-56 space-y-1.5 overflow-auto rounded-lg border border-border bg-raised/30 p-2 text-[11px]">
                      {[...(hs?.recent ?? [])].reverse().slice(0, 24).map((line, i) => (
                        <li key={`${line.at}-${i}`} className="flex gap-2 text-fg-dim">
                          <span className="shrink-0 font-mono text-muted">{shortTime(line.at)}</span>
                          <span className="shrink-0 text-amber">{line.type}</span>
                          <span className="min-w-0 truncate text-fg">{line.detail}</span>
                        </li>
                      ))}
                      {!hs?.recent?.length ? (
                        <li className="px-1 py-3 text-muted">Nothing yet.</li>
                      ) : null}
                    </ul>
                  </div>
                  <div>
                    <div className="mb-2 flex items-center justify-between gap-2">
                      <p className="text-xs font-medium text-fg">Recently learned</p>
                      <Link to="/app/memory" className="text-[11px] text-ember hover:underline">
                        Review →
                      </Link>
                    </div>
                    <ul className="max-h-56 space-y-1.5 overflow-auto rounded-lg border border-border bg-raised/30 p-2 text-[11px]">
                      {(harvestedMemories.data ?? [])
                        .filter(
                          (m) =>
                            (m.source || '').includes('processor') ||
                            (m.source || '').includes('daemon') ||
                            (m.source || '').includes('harvest'),
                        )
                        .slice(0, 8)
                        .map((m) => (
                          <li key={m.id} className="border-b border-border/60 pb-1.5 last:border-0">
                            <span className="font-mono text-fg">{m.key}</span>
                            <p className="mt-0.5 line-clamp-2 text-fg-dim">{m.content}</p>
                          </li>
                        ))}
                      {!projectId ? (
                        <li className="px-1 py-3 text-muted">Pick a project to see what was learned.</li>
                      ) : null}
                    </ul>
                  </div>
                </div>
              </>
            ) : (
              <div className="space-y-3">
                <p className="text-sm text-fg-dim">
                  Details appear once Nexus on this computer is signed in with this account, so nobody
                  can see another person&apos;s chats.
                </p>
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  onClick={() => void bridge.local.refetch()}
                  disabled={bridge.local.isFetching}
                >
                  <RefreshCw size={14} className={bridge.local.isFetching ? 'animate-spin' : ''} aria-hidden />
                  {bridge.local.isFetching ? 'Checking…' : 'Check this computer'}
                </Button>
                {bridge.local.isFetched && !bridge.local.isFetching && !bridge.local.data ? (
                  <p className="text-xs text-muted" role="status">
                    Nexus didn&apos;t answer on this computer. Once the app is signed in, it reports in
                    through your account.
                  </p>
                ) : null}
              </div>
            )}
          </GlassPanel>
      </Advanced>

    </div>
  )
}

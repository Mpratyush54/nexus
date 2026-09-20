import { motion } from 'framer-motion'
import {
  Check,
  ExternalLink,
  HardDrive,
  Plug,
  Radio,
  RefreshCw,
  ScanSearch,
} from 'lucide-react'
import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
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
        label: 'Desktop installed',
        done: Boolean(detected) || Boolean(bridge.serverView.data?.online),
        hint:
          Boolean(detected) || bridge.serverView.data?.online
            ? 'Nexus Desktop detected on this machine'
            : 'Run the install command below',
      },
      {
        id: 'same-account',
        label: 'Same account',
        done: bridge.identity === 'match',
        hint:
          bridge.identity === 'match'
            ? `Linked as ${user?.username ?? 'you'}`
            : bridge.identity === 'mismatch'
              ? 'Desktop is a different user — re-link'
              : 'Sign in Desktop with this portal account',
      },
      {
        id: 'scan',
        label: 'Auto-harvest',
        done: Boolean(hs?.running && hs.designated),
        hint: hs?.running
          ? `${hs.last_scan_files ?? 0} files · ${hs.last_scan_turns ?? 0} new turns · ${hs.turns_emitted ?? 0} lifetime`
          : 'Waiting for harvest pipeline',
      },
    ]
  }, [detected, bridge.serverView.data?.online, bridge.identity, hs, user?.username])

  const onBindFolder = () => {
    if (!bridge.trusted) {
      push({
        title: 'Accounts must match',
        detail: 'Sign Desktop in as the same user as this portal first',
        tone: 'danger',
      })
      return
    }
    if (!folder) {
      push({ title: 'No folder detected', detail: 'Start Nexus Desktop first', tone: 'danger' })
      return
    }
    bind.mutate(folder, {
      onSuccess: (p) =>
        push({ title: 'Workspace linked', detail: p.display_name || p.folder_name || p.id, tone: 'teal' }),
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
        detail: 'Desktop and portal accounts do not match',
        tone: 'danger',
      })
      return
    }
    scanNow.mutate(undefined, {
      onSuccess: (st) =>
        push({
          title: 'Scan complete',
          detail: st.message || `${st.last_scan_files ?? 0} files · ${st.last_scan_turns ?? 0} turns`,
          tone: 'teal',
        }),
      onError: (err) =>
        push({
          title: 'Scan failed',
          detail: err instanceof Error ? err.message : 'Daemon unreachable',
          tone: 'danger',
        }),
    })
  }

  const onRelink = () => {
    const base = detected?.baseUrl
    if (!base) {
      push({ title: 'Desktop offline', detail: 'Install / start Nexus Desktop first', tone: 'danger' })
      return
    }
    void daemonApi
      .startBrowserLogin(base)
      .then(() =>
        push({
          title: 'Sign-in opened',
          detail: 'Use the same account as this portal',
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
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Desktop setup</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-dim">
          Install Nexus Desktop once — Start Menu and Startup shortcuts are included. Sign in with
          the <span className="text-fg">same</span> account as this portal (
          <span className="font-mono">{user?.username ?? 'you'}</span>), then diagnose harvest here.
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

      {/* Install instructions — always visible */}
      <GlassPanel className="space-y-4 p-5">
        <DesktopInstallPicker
          description="One command installs tray + daemon + CLI and registers login/startup for your OS."
        />
        <div className="flex flex-wrap gap-2">
          <Button type="button" size="sm" variant="secondary" onClick={onRelink}>
            Sign in / re-link Desktop
          </Button>
          <a
            className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border px-3 text-xs text-fg-dim hover:border-border-strong hover:text-fg"
            href={detected?.baseUrl || 'http://127.0.0.1:7272/'}
            target="_blank"
            rel="noreferrer"
            title="Local Desktop status page on this PC (loopback). Not a shared file browser."
          >
            Desktop status page <ExternalLink size={12} />
          </a>
        </div>
        <p className="text-[11px] text-muted">
          The status page at <span className="font-mono">127.0.0.1:7272</span> is only on{' '}
          <span className="text-fg">this machine</span> — it shows whether Desktop is signed in and
          which folder it watches. It is not part of the cloud product UI.
        </p>
      </GlassPanel>

      {/* Live harvest — only when trusted */}
      <GlassPanel className="space-y-5 p-5" glow={hs?.running ? 'amber' : 'none'}>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex items-center gap-2">
            <ScanSearch size={18} className="text-amber" />
            <div>
              <h2 className="text-sm font-medium text-fg">Live harvest</h2>
              <p className="mt-0.5 text-xs text-fg-dim">
                {!bridge.trusted
                  ? 'Unlocks when Desktop is signed in as this portal user'
                  : hs?.message ||
                    (harvestOnline
                      ? 'Reading agent transcripts from this machine…'
                      : 'Start Nexus Desktop to stream scan status here')}
              </p>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <StatusPill tone={hs?.running ? 'teal' : harvestOnline ? 'amber' : 'danger'}>
              {hs?.running ? 'scanning' : harvestOnline ? 'daemon only' : 'offline'}
            </StatusPill>
            {bridge.trusted ? (
              <StatusPill tone={hs?.designated ? 'teal' : 'neutral'}>
                {hs?.designated ? 'designated' : 'not designated'}
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
                  label: 'Files · new turns',
                  value: `${hs?.last_scan_files ?? 0} · ${hs?.last_scan_turns ?? 0}`,
                },
                { label: 'Turns emitted', value: String(hs?.turns_emitted ?? 0) },
                { label: 'Active harnesses', value: String(activeAgents) },
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
                  <span className="text-muted">Poll</span> every {hs?.poll_seconds ?? 30}s
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
                {scanNow.isPending ? 'Scanning…' : 'Scan now'}
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
              <p className="mb-2 text-xs font-medium text-fg">Harnesses</p>
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
                  <span className="text-xs text-muted">Waiting for daemon harness list…</span>
                ) : null}
              </div>
            </div>

            <div className="grid gap-4 lg:grid-cols-2">
              <div>
                <p className="mb-2 text-xs font-medium text-fg">Daemon activity</p>
                <ul className="max-h-56 space-y-1.5 overflow-auto rounded-lg border border-border bg-raised/30 p-2 text-[11px]">
                  {[...(hs?.recent ?? [])].reverse().slice(0, 24).map((line, i) => (
                    <li key={`${line.at}-${i}`} className="flex gap-2 text-fg-dim">
                      <span className="shrink-0 font-mono text-muted">{shortTime(line.at)}</span>
                      <span className="shrink-0 text-amber">{line.type}</span>
                      <span className="min-w-0 truncate text-fg">{line.detail}</span>
                    </li>
                  ))}
                  {!hs?.recent?.length ? (
                    <li className="px-1 py-3 text-muted">No harvest events yet.</li>
                  ) : null}
                </ul>
              </div>
              <div>
                <div className="mb-2 flex items-center justify-between gap-2">
                  <p className="text-xs font-medium text-fg">Recent portal memories</p>
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
                    <li className="px-1 py-3 text-muted">Link a project to see harvested memories.</li>
                  ) : null}
                </ul>
              </div>
            </div>
          </>
        ) : (
          <p className="text-sm text-fg-dim">
            Harvest diagnostics stay hidden until Desktop and portal share the same account. That
            stops one login from reading another user&apos;s local chats.
          </p>
        )}
      </GlassPanel>

      <GlassPanel className="flex flex-wrap items-center justify-between gap-3 p-4">
        <div className="flex items-center gap-2 text-sm text-fg-dim">
          <Radio size={16} className="text-amber" />
          <HardDrive size={16} className="text-muted" />
          MCP tokens live on{' '}
          <Link to="/app/agents" className="text-ember hover:underline">
            Agents
          </Link>
          — not required for harvest.
        </div>
        <Link to="/app/memory">
          <Button type="button" size="sm" variant="secondary">
            Review memory
          </Button>
        </Link>
      </GlassPanel>
    </div>
  )
}

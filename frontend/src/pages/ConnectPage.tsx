import { motion } from 'framer-motion'
import {
  Cable,
  Check,
  Copy,
  Download,
  ExternalLink,
  HardDrive,
  MessageSquare,
  Plug,
  Radio,
  RefreshCw,
  ScanSearch,
  Sparkles,
} from 'lucide-react'
import { useMemo, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { KNOWN_MCP_TOOLS } from '@/api/agents'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import { useCreateToken } from '@/hooks/useAuthMutations'
import { useMCPFeed, useUpsertAgent } from '@/hooks/useAgents'
import {
  useBindLocalWorkspace,
  useLocalDaemonAutodetect,
  useLocalGitStatus,
  useLocalHarvest,
  useLocalWorkspaceView,
  useTriggerHarvestScan,
} from '@/hooks/useDaemon'
import { useCreateSession, useSessions } from '@/hooks/useSessions'
import { useMemorySearch } from '@/hooks/useMemory'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'

const INSTALL_PS1 =
  'irm https://central-memory-releases.s3.ap-south-1.amazonaws.com/cli/0.1.0/install-windows.ps1 | iex'

const MEM_PATH_WIN = String.raw`%LOCALAPPDATA%\Nexus\bin\mem.exe`

type McpConfigMode = 'cloud' | 'local'

function publicApiBase() {
  const base = (import.meta.env.VITE_API_BASE as string | undefined) ?? ''
  if (/^https?:\/\//i.test(base)) return base.replace(/\/$/, '')
  return 'https://api-nexus.pratyushes.dev'
}

function folderFromPath(path?: string) {
  if (!path) return ''
  const parts = path.replace(/[/\\]+$/, '').split(/[/\\]/)
  return parts[parts.length - 1] || ''
}

/** Cloud MCP hits HTTPS /v1/agent/mcp — no local mem.exe path required. */
function buildCloudMcpJson(token: string, projectId?: string | null) {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${token}`,
  }
  if (projectId) headers['X-Nexus-Project'] = projectId
  return JSON.stringify(
    {
      mcpServers: {
        nexus: {
          url: `${publicApiBase()}/v1/agent/mcp`,
          headers,
        },
      },
    },
    null,
    2,
  )
}

/** Local stdio MCP when Nexus Desktop / mem is installed on this machine. */
function buildLocalMcpJson(token: string, agent = 'cursor', projectId?: string | null) {
  const env: Record<string, string> = {
    NEXUS_SERVER: publicApiBase(),
    NEXUS_AGENT: agent,
    NEXUS_TOKEN: token,
  }
  if (projectId) env.NEXUS_PROJECT = projectId
  return JSON.stringify(
    {
      mcpServers: {
        nexus: {
          command: MEM_PATH_WIN,
          args: ['mcp'],
          env,
        },
      },
    },
    null,
    2,
  )
}

function buildMcpJson(
  token: string,
  mode: McpConfigMode,
  agent = 'cursor',
  projectId?: string | null,
) {
  return mode === 'local'
    ? buildLocalMcpJson(token, agent, projectId)
    : buildCloudMcpJson(token, projectId)
}

async function copyText(text: string) {
  await navigator.clipboard.writeText(text)
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
  const local = useLocalDaemonAutodetect()
  const serverView = useLocalWorkspaceView()
  const bind = useBindLocalWorkspace()
  const createToken = useCreateToken()
  const upsertAgent = useUpsertAgent()
  const feed = useMCPFeed()
  const sessions = useSessions()
  const createSession = useCreateSession()

  const bridgeUrl =
    local.data?.baseUrl ||
    local.data?.status.proxy_url ||
    serverView.data?.proxy_url ||
    undefined
  const harvest = useLocalHarvest(bridgeUrl)
  const git = useLocalGitStatus(bridgeUrl)
  const scanNow = useTriggerHarvestScan(bridgeUrl)
  const harvestedMemories = useMemorySearch('')

  const [mcpJson, setMcpJson] = useState<string | null>(null)
  const [mintedToken, setMintedToken] = useState<string | null>(null)
  const [mcpMode, setMcpMode] = useState<McpConfigMode>('cloud')
  const [sessionTitle, setSessionTitle] = useState('Cursor work')

  const detected = local.data
  const folder = folderFromPath(detected?.status.root || harvest.data?.root)
  const harvestOnline = Boolean(detected) || Boolean(serverView.data?.online)
  const mcpCalls = feed.data?.length ?? 0
  const sessionCount = sessions.data?.length ?? 0
  const hs = harvest.data
  const activeAgents = (hs?.agents ?? []).filter((a) => a.active).length

  const steps = useMemo(() => {
    return [
      {
        id: 'desktop',
        label: 'Desktop agent',
        done: harvestOnline,
        hint: harvestOnline ? 'Daemon detected on this machine' : 'Install / start Nexus Desktop',
      },
      {
        id: 'scan',
        label: 'Auto-harvest',
        done: Boolean(hs?.running && hs.designated),
        hint: hs?.running
          ? `${hs.last_scan_files ?? 0} files · ${hs.proposals_saved ?? 0} uploaded`
          : 'Waiting for daemon harvest pipeline',
      },
      {
        id: 'mcp',
        label: 'Editor MCP',
        done: Boolean(mintedToken) || mcpCalls > 0,
        hint: mcpCalls > 0 ? `${mcpCalls} recent tool call(s)` : 'Optional — explicit memory_write',
      },
      {
        id: 'session',
        label: 'Active session',
        done: sessionCount > 0,
        hint: sessionCount > 0 ? `${sessionCount} session(s)` : 'Open a session to track work',
      },
    ]
  }, [harvestOnline, hs, mintedToken, mcpCalls, sessionCount])

  const rebuildMcpJson = (tok: string, mode: McpConfigMode) => {
    setMcpJson(buildMcpJson(tok, mode, 'cursor', projectId))
  }

  const onMintMcp = () => {
    createToken.mutate('Cursor MCP', {
      onSuccess: (res) => {
        const tok = res.token
        setMintedToken(tok)
        rebuildMcpJson(tok, mcpMode)
        if (projectId) {
          const tools: Record<string, boolean> = {}
          for (const t of KNOWN_MCP_TOOLS) tools[t] = true
          upsertAgent.mutate({
            agentId: 'cursor',
            input: { mode: 'full', rate_limit: 120, tools },
          })
        }
        push({
          title: 'MCP token ready',
          detail:
            mcpMode === 'cloud'
              ? 'Cloud MCP — paste into .cursor/mcp.json (no mem.exe path). Reload Cursor.'
              : 'Local MCP — requires Nexus Desktop. Paste into .cursor/mcp.json then reload.',
          tone: 'teal',
        })
      },
      onError: (err) =>
        push({
          title: 'Could not mint token',
          detail: err instanceof ApiError ? err.message : 'Unknown error',
          tone: 'danger',
        }),
    })
  }

  const onSelectMcpMode = (mode: McpConfigMode) => {
    setMcpMode(mode)
    if (mintedToken) rebuildMcpJson(mintedToken, mode)
  }

  const onBindFolder = () => {
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

  const onCreateSession = (e: FormEvent) => {
    e.preventDefault()
    createSession.mutate(sessionTitle.trim() || 'Untitled session', {
      onSuccess: (s) => {
        push({ title: 'Session opened', detail: s.title || s.id, tone: 'teal' })
        setSessionTitle('')
      },
      onError: (err) =>
        push({
          title: 'Create failed',
          detail: err instanceof ApiError ? err.message : 'Unknown error',
          tone: 'danger',
        }),
    })
  }

  const downloadMcp = () => {
    if (!mcpJson) return
    const blob = new Blob([mcpJson], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'mcp.json'
    a.click()
    URL.revokeObjectURL(url)
  }

  const gitBranch = git.data?.branch
  const gitDirty = Boolean(git.data?.dirty ?? git.data?.is_dirty)

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Desktop setup</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-dim">
          Install Nexus Desktop, bind this folder, and diagnose harvest. Day-to-day work lives on{' '}
          <Link to="/app/dashboard" className="text-ember hover:underline">
            Home
          </Link>
          — project memories, scanner, and multi-project switching.
        </p>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
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

      {/* Live harvest — primary surface */}
      <GlassPanel className="space-y-5 p-5" glow={hs?.running ? 'amber' : 'none'}>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex items-center gap-2">
            <ScanSearch size={18} className="text-amber" />
            <div>
              <h2 className="text-sm font-medium text-fg">Live harvest</h2>
              <p className="mt-0.5 text-xs text-fg-dim">
                {hs?.message ||
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
            <StatusPill tone={hs?.designated ? 'teal' : 'neutral'}>
              {hs?.designated ? 'designated' : 'not designated'}
            </StatusPill>
          </div>
        </div>

        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {[
            { label: 'Last scan', value: shortTime(hs?.last_scan_at) },
            { label: 'Files / turns', value: `${hs?.last_scan_files ?? 0} / ${hs?.last_scan_turns ?? 0}` },
            { label: 'Uploaded', value: String(hs?.proposals_saved ?? 0) },
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
              <span className="text-muted">Machine</span> {detected?.status.machine_id || '—'}
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
            <p>
              <span className="text-muted">Tracked</span> {hs?.tracked_files ?? 0} files ·{' '}
              {hs?.active_sessions ?? 0} sessions
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
              void local.refetch()
              void harvest.refetch()
              void git.refetch()
            }}
            disabled={local.isFetching || harvest.isFetching}
          >
            <RefreshCw
              size={14}
              className={local.isFetching || harvest.isFetching ? 'animate-spin' : ''}
            />
            Refresh
          </Button>
          {folder ? (
            <Button type="button" size="sm" variant="secondary" onClick={onBindFolder} disabled={bind.isPending}>
              <Plug size={14} />
              Use this folder as project
            </Button>
          ) : null}
          <a
            className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border px-3 text-xs text-fg-dim hover:border-border-strong hover:text-fg"
            href={bridgeUrl || 'http://127.0.0.1:7272/'}
            target="_blank"
            rel="noreferrer"
          >
            Local status <ExternalLink size={12} />
          </a>
        </div>

        {hs?.last_scan_error || hs?.last_proposal_error ? (
          <p className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
            {hs.last_proposal_error || hs.last_scan_error}
          </p>
        ) : null}

        <div>
          <p className="mb-2 text-xs font-medium text-fg">Harnesses</p>
          <p className="mb-2 text-[11px] text-muted">
            Only transcripts that match this folder ({folder || hs?.root || 'bound root'}) count.
            Claude/Codex sessions for other D: projects stay at 0 until you bind that folder.
          </p>
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
                title={`${a.format}${a.cwd_match ? ' · cwd-match' : ''} · ${a.dirs} dirs`}
              >
                <span className={a.active ? 'text-teal' : ''}>{a.name}</span>
                <span className="font-mono text-muted">{a.format}</span>
                {a.files_seen > 0 ? (
                  <span className="font-mono text-fg">{a.files_seen}</span>
                ) : (
                  <span className="font-mono text-muted">0</span>
                )}
              </span>
            ))}
            {!hs?.agents?.length && !harvestOnline ? (
              <span className="text-xs text-muted">Online daemon will list Claude, Cursor, Codex, OpenCode, …</span>
            ) : null}
          </div>
        </div>

        <div>
          <p className="mb-2 text-xs font-medium text-fg">Matched JSONL / SQLite</p>
          <ul className="max-h-48 space-y-1 overflow-auto rounded-lg border border-border bg-raised/30 p-2 font-mono text-[11px]">
            {(hs?.files ?? []).map((f) => (
              <li key={f.path} className="flex gap-2 text-fg-dim">
                <span className="shrink-0 text-amber">{f.agent}</span>
                <span className="shrink-0 text-muted">{f.format}</span>
                <span className="min-w-0 truncate text-fg" title={f.path}>
                  {f.name}
                </span>
              </li>
            ))}
            {!hs?.files?.length ? (
              <li className="px-1 py-3 text-muted">
                No files matched this workspace yet. Cursor chats for this folder appear as
                <span className="text-fg"> *.jsonl</span> under agent-transcripts; SQLite is
                tracked for liveness (row text needs sqlite extract).
              </li>
            ) : null}
          </ul>
        </div>

        <div className="grid gap-4 lg:grid-cols-2">
          <div>
            <p className="mb-2 text-xs font-medium text-fg">Daemon activity</p>
            <ul className="max-h-56 space-y-1.5 overflow-auto rounded-lg border border-border bg-raised/30 p-2 text-[11px]">
              {[...(hs?.recent ?? [])].reverse().slice(0, 24).map((line, i) => (
                <li key={`${line.at}-${i}`} className="flex gap-2 text-fg-dim">
                  <span className="shrink-0 font-mono text-muted">{shortTime(line.at)}</span>
                  <span className="shrink-0 text-amber">{line.type}</span>
                  {line.agent ? <span className="text-muted">{line.agent}</span> : null}
                  <span className="min-w-0 truncate text-fg">{line.detail}</span>
                </li>
              ))}
              {!hs?.recent?.length ? (
                <li className="px-1 py-3 text-muted">No harvest events yet — chat in Cursor/Claude/Codex or hit Scan now.</li>
              ) : null}
            </ul>
          </div>
          <div>
            <div className="mb-2 flex items-center justify-between gap-2">
              <p className="text-xs font-medium text-fg">Recent portal memories</p>
              <Link to="/app/memory" className="text-[11px] text-ember hover:underline">
                Review queue →
              </Link>
            </div>
            <ul className="max-h-56 space-y-1.5 overflow-auto rounded-lg border border-border bg-raised/30 p-2 text-[11px]">
              {(harvestedMemories.data ?? [])
                .filter((m) => (m.source || '').includes('processor') || (m.source || '').includes('daemon'))
                .slice(0, 8)
                .map((m) => (
                  <li key={m.id} className="border-b border-border/60 pb-1.5 last:border-0">
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-mono text-fg">{m.key}</span>
                      <StatusPill tone={m.status === 'PROPOSED' ? 'amber' : 'teal'}>
                        {m.status || '—'}
                      </StatusPill>
                    </div>
                    <p className="mt-0.5 line-clamp-2 text-fg-dim">{m.content}</p>
                  </li>
                ))}
              {!projectId ? (
                <li className="px-1 py-3 text-muted">Link a project to see harvested PROPOSED rows.</li>
              ) : !(harvestedMemories.data ?? []).some(
                  (m) => (m.source || '').includes('processor') || (m.source || '').includes('daemon'),
                ) ? (
                <li className="px-1 py-3 text-muted">No harvested memories yet — they appear after a scan finds chat turns.</li>
              ) : null}
            </ul>
          </div>
        </div>

        {!detected ? (
          <div className="rounded-lg border border-dashed border-border px-3 py-2.5">
            <p className="text-xs font-medium text-fg">Windows install</p>
            <pre className="mt-2 overflow-x-auto text-[11px] leading-relaxed text-fg-dim">{INSTALL_PS1}</pre>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="mt-2"
              onClick={() =>
                void copyText(INSTALL_PS1).then(() =>
                  push({ title: 'Copied', detail: 'Paste into PowerShell' }),
                )
              }
            >
              <Copy size={14} /> Copy install command
            </Button>
          </div>
        ) : null}
      </GlassPanel>

      <div className="grid gap-6 lg:grid-cols-2">
        <GlassPanel className="space-y-4 p-5">
          <div className="flex items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <Cable size={16} className="text-amber" />
              <h2 className="text-sm font-medium text-fg">Connect MCP (optional)</h2>
            </div>
            <StatusPill tone={mcpCalls > 0 ? 'teal' : mintedToken ? 'amber' : 'neutral'}>
              {mcpCalls > 0 ? 'active' : mintedToken ? 'configured' : 'not connected'}
            </StatusPill>
          </div>

          <p className="text-sm text-fg-dim">
            MCP is for explicit <span className="font-mono text-xs">memory_write</span>. Prefer{' '}
            <span className="text-fg">Cloud</span> (HTTPS{' '}
            <span className="font-mono text-xs">/v1/agent/mcp</span>) so agents never hunt for{' '}
            <span className="font-mono text-xs">mem.exe</span>. Use Local only when Nexus Desktop is
            installed. Auto-harvest above does not need MCP.
          </p>

          <div className="flex flex-wrap gap-2" role="group" aria-label="MCP config mode">
            <Button
              type="button"
              size="sm"
              variant={mcpMode === 'cloud' ? 'primary' : 'secondary'}
              onClick={() => onSelectMcpMode('cloud')}
            >
              Cloud API
            </Button>
            <Button
              type="button"
              size="sm"
              variant={mcpMode === 'local' ? 'primary' : 'secondary'}
              onClick={() => onSelectMcpMode('local')}
            >
              Local desktop
            </Button>
          </div>

          <div className="flex flex-wrap gap-2">
            <Button type="button" size="sm" onClick={onMintMcp} disabled={createToken.isPending}>
              <Sparkles size={14} />
              {createToken.isPending ? 'Minting…' : 'Generate MCP config'}
            </Button>
            {mcpJson ? (
              <>
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  onClick={() =>
                    void copyText(mcpJson).then(() => push({ title: 'Copied mcp.json' }))
                  }
                >
                  <Copy size={14} /> Copy JSON
                </Button>
                <Button type="button" size="sm" variant="secondary" onClick={downloadMcp}>
                  <Download size={14} /> Download
                </Button>
              </>
            ) : null}
          </div>

          {mcpJson ? (
            <>
              <p className="text-xs text-muted">
                {mcpMode === 'cloud'
                  ? `Paste into .cursor/mcp.json · ${publicApiBase()}/v1/agent/mcp` +
                    (projectId ? ` · project ${projectId.slice(0, 8)}…` : '')
                  : 'Paste into .cursor/mcp.json · expands %LOCALAPPDATA% on Windows'}
              </p>
              <pre className="max-h-48 overflow-auto rounded-lg border border-border bg-raised/60 p-3 text-[11px] leading-relaxed text-fg-dim">
                {mintedToken ? mcpJson.replaceAll(mintedToken, 'nxs_…REDACTED…') : mcpJson}
              </pre>
            </>
          ) : (
            <p className="text-xs text-muted">Signed in as {user?.username ?? 'you'}.</p>
          )}
        </GlassPanel>

        <GlassPanel className="space-y-4 p-5">
          <div className="flex items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <MessageSquare size={16} className="text-amber" />
              <h2 className="text-sm font-medium text-fg">Sessions</h2>
            </div>
            <Link to="/app/sessions" className="text-xs text-ember hover:underline">
              Open Sessions →
            </Link>
          </div>

          {!projectId ? (
            <p className="text-sm text-fg-dim">Link a workspace project first (Use this folder).</p>
          ) : (
            <>
              <form onSubmit={onCreateSession} className="flex flex-wrap gap-2">
                <input
                  value={sessionTitle}
                  onChange={(e) => setSessionTitle(e.target.value)}
                  placeholder="Session title"
                  className="h-9 min-w-[12rem] flex-1 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
                />
                <Button type="submit" size="sm" disabled={createSession.isPending}>
                  Start session
                </Button>
              </form>
              <ul className="divide-y divide-border rounded-lg border border-border">
                {(sessions.data ?? []).slice(0, 5).map((s) => (
                  <li key={s.id} className="flex items-center justify-between gap-2 px-3 py-2 text-sm">
                    <span className="truncate text-fg">{s.title || s.id.slice(0, 8)}</span>
                    <span className="shrink-0 text-xs text-muted">
                      {s.created_at ? formatRelative(s.created_at) : ''}
                    </span>
                  </li>
                ))}
                {!sessions.data?.length ? (
                  <li className="px-3 py-4 text-sm text-muted">No sessions yet — start one above.</li>
                ) : null}
              </ul>
            </>
          )}
        </GlassPanel>
      </div>

      <GlassPanel className="flex flex-wrap items-center justify-between gap-3 p-4">
        <div className="flex items-center gap-2 text-sm text-fg-dim">
          <Radio size={16} className="text-amber" />
          <HardDrive size={16} className="text-muted" />
          Desktop scan + portal review is the product loop — MCP is optional.
        </div>
        <div className="flex flex-wrap gap-2">
          <Link to="/app/memory">
            <Button type="button" size="sm" variant="secondary">
              Review memory
            </Button>
          </Link>
          <Link to="/app/agents">
            <Button type="button" size="sm" variant="secondary">
              Agent activity
            </Button>
          </Link>
          <Link to="/app/activity">
            <Button type="button" size="sm" variant="ghost">
              Activity log
            </Button>
          </Link>
        </div>
      </GlassPanel>
    </div>
  )
}

import { apiRequest } from '@/lib/api-client'

export type LocalWorkspaceView = {
  online: boolean
  project_id: string
  source?: string
  stale?: boolean
  workspace_id?: string
  path?: string
  branch?: string
  commit_sha?: string
  is_dirty?: boolean
  machine_id?: string
  /** Same-machine browser bridge advertised by the daemon via heartbeat. */
  proxy_url?: string
  git_log?: string
  bridge_updated_at?: string
  workspace?: {
    id?: string
    path?: string
    branch?: string
    commit_sha?: string
    is_dirty?: boolean
    machine_id?: string
    last_seen?: string
  }
}

/** Shape returned by the local CORS proxy GET /local/status. */
export type LocalDaemonStatus = {
  ok?: boolean
  connected?: boolean
  server_url?: string
  app_url?: string
  user_id?: string
  username?: string
  has_token?: boolean
  workspace_id?: string
  root?: string
  machine_id?: string
  proxy_url?: string
  message?: string
  status_page?: string
}

/** Live auto-sync / scan snapshot from GET|POST /local/harvest. */
export type HarvestAgentStat = {
  name: string
  format: string
  cwd_match?: boolean
  dirs: number
  files_seen: number
  active: boolean
}

export type HarvestLogLine = {
  at: string
  type: string
  agent?: string
  detail?: string
}

export type HarvestFileHit = {
  agent: string
  format: string
  path: string
  name: string
}

export type HarvestStatus = {
  ok?: boolean
  running?: boolean
  designated?: boolean
  project_id?: string
  root?: string
  poll_seconds?: number
  idle_seconds?: number
  last_scan_at?: string
  last_scan_files?: number
  last_scan_turns?: number
  last_scan_error?: string
  tracked_files?: number
  active_sessions?: number
  turns_emitted?: number
  completions?: number
  proposals_saved?: number
  proposal_errors?: number
  last_proposal_error?: string
  last_event_at?: string
  agents?: HarvestAgentStat[]
  files?: HarvestFileHit[]
  recent?: HarvestLogLine[]
  message?: string
}

export type LocalGitStatus = {
  branch?: string
  commit?: string
  commit_sha?: string
  dirty?: boolean
  is_dirty?: boolean
  porcelain?: string
}

/** Localhost candidates the PWA probes for same-machine desktop agents. */
export const LOCAL_DAEMON_CANDIDATES = [
  'http://127.0.0.1:7272',
  'http://localhost:7272',
] as const

/**
 * Local workspace state is relayed daemon → central server → PWA.
 * Optional file reads use the advertised `proxy_url` when the browser can
 * reach it — never a hardcoded :7272 as the *only* path; Connect also
 * autodetects loopback.
 */
export const daemonApi = {
  localView(projectId: string) {
    return apiRequest<LocalWorkspaceView>(`/workspaces/${projectId}/local`)
  },

  async bridgeFetch<T>(
    proxyUrl: string,
    path: string,
    init?: RequestInit,
  ): Promise<T> {
    const base = proxyUrl.replace(/\/$/, '')
    const res = await fetch(`${base}${path}`, {
      ...init,
      signal: init?.signal ?? AbortSignal.timeout(4_000),
      headers: {
        Accept: 'application/json',
        ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
        ...init?.headers,
      },
    })
    if (!res.ok) {
      const text = await res.text().catch(() => '')
      throw new Error(text || `bridge ${res.status}`)
    }
    return (await res.json()) as T
  },

  readFile(proxyUrl: string, path: string) {
    return this.bridgeFetch<{ path: string; size: number; content: string }>(
      proxyUrl,
      '/local/file/read',
      { method: 'POST', body: JSON.stringify({ path }) },
    )
  },

  async probeBridge(proxyUrl: string, signal?: AbortSignal): Promise<boolean> {
    try {
      const base = proxyUrl.replace(/\/$/, '')
      const res = await fetch(`${base}/local/healthz`, {
        signal: signal ?? AbortSignal.timeout(2_000),
      })
      if (!res.ok) return false
      const body = (await res.json()) as { ok?: boolean }
      return Boolean(body.ok)
    } catch {
      return false
    }
  },

  async probeLocalStatus(
    baseUrl: string,
    signal?: AbortSignal,
  ): Promise<LocalDaemonStatus | null> {
    try {
      const base = baseUrl.replace(/\/$/, '')
      const res = await fetch(`${base}/local/status`, {
        signal: signal ?? AbortSignal.timeout(2_000),
      })
      if (!res.ok) return null
      return (await res.json()) as LocalDaemonStatus
    } catch {
      return null
    }
  },

  /** Autodetect a same-machine Nexus Desktop / daemon CORS proxy. */
  async autodetectLocal(
    signal?: AbortSignal,
  ): Promise<{ baseUrl: string; status: LocalDaemonStatus } | null> {
    for (const base of LOCAL_DAEMON_CANDIDATES) {
      const status = await this.probeLocalStatus(base, signal)
      if (status?.ok || status?.machine_id || status?.root) {
        return { baseUrl: base, status }
      }
      // Fallback healthz-only (older daemons without /local/status)
      if (await this.probeBridge(base, signal)) {
        return {
          baseUrl: base,
          status: { ok: true, proxy_url: base, message: 'Daemon reachable' },
        }
      }
    }
    return null
  },

  startBrowserLogin(proxyUrl: string) {
    return this.bridgeFetch<{ ok?: boolean; message?: string }>(
      proxyUrl,
      '/local/browser-login',
      { method: 'POST' },
    )
  },

  harvestStatus(proxyUrl: string, signal?: AbortSignal) {
    return this.bridgeFetch<HarvestStatus>(proxyUrl, '/local/harvest', { signal })
  },

  /** Force an immediate transcript scan (Connect “Scan now”). */
  harvestScanNow(proxyUrl: string) {
    return this.bridgeFetch<HarvestStatus>(proxyUrl, '/local/harvest', { method: 'POST' })
  },

  gitStatus(proxyUrl: string, signal?: AbortSignal) {
    return this.bridgeFetch<LocalGitStatus>(proxyUrl, '/local/git/status', { signal })
  },
}

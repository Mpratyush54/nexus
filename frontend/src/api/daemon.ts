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

/**
 * Local workspace state is relayed daemon → central server → PWA.
 * Optional file reads use the advertised `proxy_url` when the browser can
 * reach it — never a hardcoded :7272.
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
}

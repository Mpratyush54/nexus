const DAEMON_BASE = 'http://127.0.0.1:7272'

export type LocalWorkspace = {
  project?: string
  path?: string
  branch?: string
  commit?: string
  is_dirty?: boolean
  machine_id?: string
  workspace_id?: string
}

export type LocalGitStatus = {
  branch?: string
  commit?: string
  is_dirty?: boolean
  porcelain?: string
}

export type LocalGitLog = {
  entries?: Array<{ hash?: string; subject?: string; author?: string; date?: string }>
  log?: string
  commits?: Array<{ hash?: string; message?: string }>
}

async function localFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${DAEMON_BASE}${path}`, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `daemon ${res.status}`)
  }
  return (await res.json()) as T
}

export const daemonApi = {
  base: DAEMON_BASE,
  async healthz(signal?: AbortSignal): Promise<boolean> {
    try {
      const res = await fetch(`${DAEMON_BASE}/local/healthz`, { signal })
      if (!res.ok) return false
      const body = (await res.json()) as { ok?: boolean }
      return Boolean(body.ok)
    } catch {
      return false
    }
  },
  workspace() {
    return localFetch<LocalWorkspace>('/local/workspace')
  },
  gitStatus() {
    return localFetch<LocalGitStatus>('/local/git/status')
  },
  gitLog() {
    return localFetch<LocalGitLog>('/local/git/log')
  },
  readFile(path: string) {
    return localFetch<{ path: string; size: number; content: string }>('/local/file/read', {
      method: 'POST',
      body: JSON.stringify({ path }),
    })
  },
}

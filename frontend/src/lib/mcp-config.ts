const MEM_PATH_WIN = String.raw`%LOCALAPPDATA%\Nexus\bin\mem.exe`

export type McpConfigMode = 'cloud' | 'local'

export function publicApiBase() {
  const base = (import.meta.env.VITE_API_BASE as string | undefined) ?? ''
  if (/^https?:\/\//i.test(base)) return base.replace(/\/$/, '')
  return 'https://api-nexus.pratyushes.dev'
}

/** Cloud MCP hits HTTPS /v1/agent/mcp — no local mem.exe path required. */
export function buildCloudMcpJson(
  token: string,
  agent: string,
  projectId?: string | null,
) {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${token}`,
  }
  if (projectId) headers['X-Nexus-Project'] = projectId
  if (agent) headers['X-Nexus-Agent'] = agent
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
export function buildLocalMcpJson(
  token: string,
  agent = 'cursor',
  projectId?: string | null,
) {
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

export function buildMcpJson(
  token: string,
  mode: McpConfigMode,
  agent = 'cursor',
  projectId?: string | null,
) {
  return mode === 'local'
    ? buildLocalMcpJson(token, agent, projectId)
    : buildCloudMcpJson(token, agent, projectId)
}

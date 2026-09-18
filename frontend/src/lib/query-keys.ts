export const queryKeys = {
  health: ['health'] as const,
  memory: {
    all: ['memory'] as const,
    search: (projectId: string, q: string, level = '') =>
      ['memory', 'search', projectId, q, level] as const,
    history: (id: string) => ['memory', 'history', id] as const,
    shares: (id: string) => ['memory', 'shares', id] as const,
  },
  project: {
    current: ['project', 'current'] as const,
  },
  workspace: {
    active: (projectId: string) => ['workspace', 'active', projectId] as const,
    presence: (projectId: string) => ['workspace', 'presence', projectId] as const,
  },
  team: {
    members: (projectId: string) => ['team', 'members', projectId] as const,
    roles: (projectId: string) => ['team', 'roles', projectId] as const,
    github: (projectId: string) => ['team', 'github', projectId] as const,
    githubRepos: (projectId: string) => ['team', 'github-repos', projectId] as const,
  },
  dashboard: {
    project: (projectId: string) => ['dashboard', projectId] as const,
  },
  agents: {
    list: (projectId: string) => ['agents', projectId] as const,
    mcpFeed: (projectId: string) => ['agents', 'mcp', projectId] as const,
  },
  orgs: {
    all: ['orgs'] as const,
    list: ['orgs', 'list'] as const,
    detail: (id: string) => ['orgs', 'detail', id] as const,
  },
  branches: {
    list: (projectId: string) => ['branches', projectId] as const,
    diff: (projectId: string, source: string, target: string) =>
      ['branches', 'diff', projectId, source, target] as const,
  },
  sessions: {
    list: (projectId: string, activeOnly: boolean) =>
      ['sessions', projectId, activeOnly] as const,
  },
  activity: {
    list: (projectId: string, type = '', userId = '', since = '') =>
      ['activity', projectId, type, userId, since] as const,
  },
  notifications: {
    list: (projectId: string) => ['notifications', projectId] as const,
    devices: ['notifications', 'devices'] as const,
    prefs: ['notifications', 'prefs'] as const,
  },
  admin: {
    overview: ['admin', 'overview'] as const,
    users: ['admin', 'users'] as const,
    releases: ['admin', 'releases'] as const,
  },
  billing: {
    plans: ['billing', 'plans'] as const,
    me: ['billing', 'me'] as const,
    org: (orgId: string) => ['billing', 'org', orgId] as const,
    admin: ['billing', 'admin'] as const,
  },
  daemon: {
    health: ['daemon', 'health'] as const,
    workspace: ['daemon', 'workspace'] as const,
    gitStatus: ['daemon', 'gitStatus'] as const,
    gitLog: ['daemon', 'gitLog'] as const,
    local: (projectId: string) => ['daemon', 'local', projectId] as const,
    bridge: (proxyUrl: string) => ['daemon', 'bridge', proxyUrl] as const,
  },
}

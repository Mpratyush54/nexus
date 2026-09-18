export const queryKeys = {
  health: ['health'] as const,
  memory: {
    all: ['memory'] as const,
    search: (projectId: string, q: string, level = '') =>
      ['memory', 'search', projectId, q, level] as const,
    history: (id: string) => ['memory', 'history', id] as const,
  },
  project: {
    current: ['project', 'current'] as const,
  },
  workspace: {
    active: (projectId: string) => ['workspace', 'active', projectId] as const,
  },
  team: {
    members: (projectId: string) => ['team', 'members', projectId] as const,
    roles: (projectId: string) => ['team', 'roles', projectId] as const,
    github: (projectId: string) => ['team', 'github', projectId] as const,
  },
  agents: {
    list: (projectId: string) => ['agents', projectId] as const,
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
  daemon: {
    health: ['daemon', 'health'] as const,
    workspace: ['daemon', 'workspace'] as const,
    gitStatus: ['daemon', 'gitStatus'] as const,
    gitLog: ['daemon', 'gitLog'] as const,
  },
}

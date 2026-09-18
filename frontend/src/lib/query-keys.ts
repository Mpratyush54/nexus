export const queryKeys = {
  health: ['health'] as const,
  memory: {
    all: ['memory'] as const,
    search: (projectId: string, q: string) =>
      ['memory', 'search', projectId, q] as const,
  },
  project: {
    current: ['project', 'current'] as const,
  },
  workspace: {
    active: (projectId: string) => ['workspace', 'active', projectId] as const,
  },
}

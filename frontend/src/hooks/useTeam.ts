import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { teamApi } from '@/api/team'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

export function useMembers() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.team.members(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: async () => (await teamApi.listMembers(projectId!)).items,
  })
}

export function useRoles() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.team.roles(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: async () => (await teamApi.listRoles(projectId!)).items,
  })
}

export function useGitHubStatus() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.team.github(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: () => teamApi.githubStatus(projectId!),
  })
}

export function useActiveWorkspaces() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.workspace.active(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    refetchInterval: 15_000,
    queryFn: async () => {
      const res = await teamApi.activeWorkspaces(projectId!)
      if (Array.isArray(res)) return res
      if (res && typeof res === 'object' && 'items' in res) {
        return (res as { items: unknown[] }).items
      }
      if (res && typeof res === 'object' && 'id' in res) return [res]
      return []
    },
  })
}

export function useProjectPresence() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.workspace.presence(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    refetchInterval: 15_000,
    queryFn: async () => (await teamApi.presence(projectId!)).items ?? [],
  })
}

export function useGrantMember() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (userId: string) => teamApi.grantMember(projectId!, userId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.team.members(projectId ?? '') })
    },
  })
}

export function useSetMemberRole() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ userId, role }: { userId: string; role: string }) =>
      teamApi.setMemberRole(projectId!, userId, role),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.team.members(projectId ?? '') })
    },
  })
}

export function useRevokeMember() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (userId: string) => teamApi.revokeMember(projectId!, userId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.team.members(projectId ?? '') })
    },
  })
}

export function useGitHubConnect() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: {
      owner: string
      repo: string
      access_token?: string
      sync_mode?: string
    }) => teamApi.githubConnect(projectId!, input),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.team.github(projectId ?? '') })
    },
  })
}

export function useGitHubImport() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => teamApi.githubImport(projectId!),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.team.members(projectId ?? '') })
      void qc.invalidateQueries({ queryKey: queryKeys.team.github(projectId ?? '') })
    },
  })
}

export function useGitHubDisconnect() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => teamApi.githubDisconnect(projectId!),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.team.github(projectId ?? '') })
      void qc.invalidateQueries({ queryKey: queryKeys.team.githubRepos(projectId ?? '') })
    },
  })
}

export function useGitHubRepos(enabled: boolean) {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.team.githubRepos(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId) && enabled,
    queryFn: async () => (await teamApi.githubRepos(projectId!)).items ?? [],
  })
}

export function useGitHubOAuthStart() {
  const { projectId } = useAuth()
  return useMutation({
    mutationFn: () => teamApi.githubOAuthStart(projectId!),
  })
}

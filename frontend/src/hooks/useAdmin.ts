import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { platformApi, type AppRelease, type ReleaseArtifact } from '@/api/platform'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

export function useAdminOverview() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.admin.overview,
    enabled: isAuthenticated,
    queryFn: () => platformApi.overview(),
    retry: false,
  })
}

export function useAdminUsers() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.admin.users,
    enabled: isAuthenticated,
    queryFn: async () => (await platformApi.users()).items,
    retry: false,
  })
}

export function useAdminReleases() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.admin.releases,
    enabled: isAuthenticated,
    queryFn: async () => (await platformApi.releases()).items,
    retry: false,
  })
}

export function useSetPlatformAdmin() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, admin }: { id: string; admin: boolean }) => platformApi.setAdmin(id, admin),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.admin.users })
      void qc.invalidateQueries({ queryKey: queryKeys.admin.overview })
    },
  })
}

export function usePublishRelease() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (rel: {
      app: string
      version: string
      channel?: string
      notes?: string
      git_sha?: string
      artifacts?: ReleaseArtifact[]
    }) => platformApi.publish(rel),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.admin.releases })
    },
  })
}

export function useYankRelease() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ app, version }: { app: string; version: string }) => platformApi.yank(app, version),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.admin.releases })
    },
  })
}

export type { AppRelease, ReleaseArtifact }

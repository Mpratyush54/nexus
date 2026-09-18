import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { orgsApi } from '@/api/orgs'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

export function useOrgs() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.orgs.list,
    enabled: isAuthenticated,
    queryFn: async () => (await orgsApi.list()).items,
  })
}

export function useOrg(id: string | null) {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.orgs.detail(id ?? ''),
    enabled: isAuthenticated && Boolean(id),
    queryFn: () => orgsApi.get(id!),
  })
}

export function useCreateOrg() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { name: string; slug?: string }) => orgsApi.create(input),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.orgs.all })
    },
  })
}

export function useAddOrgMember(orgId: string | null) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { user_id: string; role?: string }) =>
      orgsApi.addMember(orgId!, input),
    onSuccess: () => {
      if (orgId) void qc.invalidateQueries({ queryKey: queryKeys.orgs.detail(orgId) })
    },
  })
}

export function useSetOrgMemberRole(orgId: string | null) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ userId, role }: { userId: string; role: string }) =>
      orgsApi.setMemberRole(orgId!, userId, role),
    onSuccess: () => {
      if (orgId) void qc.invalidateQueries({ queryKey: queryKeys.orgs.detail(orgId) })
    },
  })
}

export function useRemoveOrgMember(orgId: string | null) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (userId: string) => orgsApi.removeMember(orgId!, userId),
    onSuccess: () => {
      if (orgId) void qc.invalidateQueries({ queryKey: queryKeys.orgs.detail(orgId) })
    },
  })
}

export function useCreateOrgProject(orgId: string | null) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { folder_name: string; display_name?: string; canonical_url?: string }) =>
      orgsApi.createProject(orgId!, input),
    onSuccess: () => {
      if (orgId) void qc.invalidateQueries({ queryKey: queryKeys.orgs.detail(orgId) })
    },
  })
}

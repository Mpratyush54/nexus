import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { branchesApi } from '@/api/branches'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

export function useBranches() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.branches.list(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: async () => (await branchesApi.list(projectId!)).items,
  })
}

export function useBranchDiff(source: string, target: string, enabled: boolean) {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.branches.diff(projectId ?? '', source, target),
    enabled: isAuthenticated && Boolean(projectId) && enabled && Boolean(target),
    queryFn: () => branchesApi.diff(projectId!, target, source || 'main'),
  })
}

export function useCreateBranch() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { name: string; from?: string; visibility?: string }) =>
      branchesApi.create({
        project_id: projectId!,
        name: input.name,
        from: input.from,
        visibility: input.visibility,
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.branches.list(projectId ?? '') })
    },
  })
}

export function useCheckoutBranch() {
  const { projectId } = useAuth()
  return useMutation({
    mutationFn: (name: string) => branchesApi.checkout(name, projectId!),
  })
}

export function useMergeBranches() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { source: string; target: string }) =>
      branchesApi.merge({ project_id: projectId!, ...input }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.branches.list(projectId ?? '') })
    },
  })
}

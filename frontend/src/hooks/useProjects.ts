import { useQuery } from '@tanstack/react-query'
import { projectsApi } from '@/api/projects'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

/** All projects the signed-in user can open. */
export function useProjects() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.project.list,
    enabled: isAuthenticated,
    queryFn: async () => (await projectsApi.list()).items,
    staleTime: 15_000,
  })
}

/** Active project row (from the list, matched by projectId). */
export function useCurrentProject() {
  const { projectId } = useAuth()
  const projects = useProjects()
  const current = (projects.data ?? []).find((p) => p.id === projectId) ?? null
  return { ...projects, current }
}

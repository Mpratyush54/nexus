import { useQuery } from '@tanstack/react-query'
import { dashboardApi } from '@/api/dashboard'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

export function useDashboard() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.dashboard.project(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    refetchInterval: 30_000,
    queryFn: ({ signal }) => dashboardApi.get(projectId!, signal),
  })
}

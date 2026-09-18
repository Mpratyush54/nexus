import { useQuery } from '@tanstack/react-query'
import { eventsApi } from '@/api/events'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

export function useActivity(type = '', userId = '', since = '') {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.activity.list(projectId ?? '', type, userId, since),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: async ({ signal }) =>
      (
        await eventsApi.list(
          {
            projectId: projectId!,
            type: type || undefined,
            userId: userId || undefined,
            since: since || undefined,
            limit: 200,
          },
          signal,
        )
      ).items,
  })
}

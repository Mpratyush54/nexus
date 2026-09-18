import { useQuery } from '@tanstack/react-query'
import { daemonApi } from '@/api/daemon'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

/** Local workspace panel — data comes from the central server channel. */
export function useLocalWorkspaceView() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.daemon.local(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: () => daemonApi.localView(projectId!),
    refetchInterval: 15_000,
    retry: false,
  })
}

/** Optional same-machine bridge reachability (advertised proxy_url only). */
export function useBridgeReachable(proxyUrl: string | undefined) {
  return useQuery({
    queryKey: queryKeys.daemon.bridge(proxyUrl ?? ''),
    enabled: Boolean(proxyUrl),
    queryFn: ({ signal }) => daemonApi.probeBridge(proxyUrl!, signal),
    refetchInterval: 20_000,
    retry: false,
    staleTime: 5_000,
  })
}

import { useQuery } from '@tanstack/react-query'
import { daemonApi } from '@/api/daemon'
import { queryKeys } from '@/lib/query-keys'

export function useDaemonHealth() {
  return useQuery({
    queryKey: queryKeys.daemon.health,
    queryFn: ({ signal }) => daemonApi.healthz(signal),
    refetchInterval: 12_000,
    retry: false,
    staleTime: 5_000,
  })
}

export function useLocalWorkspace(enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.daemon.workspace,
    enabled,
    queryFn: () => daemonApi.workspace(),
    refetchInterval: 15_000,
    retry: false,
  })
}

export function useLocalGitStatus(enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.daemon.gitStatus,
    enabled,
    queryFn: () => daemonApi.gitStatus(),
    refetchInterval: 15_000,
    retry: false,
  })
}

export function useLocalGitLog(enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.daemon.gitLog,
    enabled,
    queryFn: () => daemonApi.gitLog(),
    refetchInterval: 30_000,
    retry: false,
  })
}

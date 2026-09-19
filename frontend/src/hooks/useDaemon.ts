import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { daemonApi } from '@/api/daemon'
import { projectsApi } from '@/api/projects'
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

/** Autodetect Nexus Desktop / daemon on loopback (Connect page). */
export function useLocalDaemonAutodetect() {
  return useQuery({
    queryKey: queryKeys.daemon.autodetect,
    queryFn: ({ signal }) => daemonApi.autodetectLocal(signal),
    refetchInterval: 5_000,
    retry: false,
    staleTime: 2_000,
  })
}

/** Live harvest / scan telemetry from the local daemon bridge. */
export function useLocalHarvest(proxyUrl: string | undefined) {
  return useQuery({
    queryKey: queryKeys.daemon.harvest(proxyUrl ?? ''),
    enabled: Boolean(proxyUrl),
    queryFn: ({ signal }) => daemonApi.harvestStatus(proxyUrl!, signal),
    refetchInterval: 4_000,
    retry: false,
    staleTime: 1_000,
  })
}

/** Force ScanAndTail now (Connect “Scan now”). */
export function useTriggerHarvestScan(proxyUrl: string | undefined) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      if (!proxyUrl) throw new Error('Daemon offline')
      return daemonApi.harvestScanNow(proxyUrl)
    },
    onSuccess: () => {
      if (proxyUrl) {
        void qc.invalidateQueries({ queryKey: queryKeys.daemon.harvest(proxyUrl) })
      }
      void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
    },
  })
}

/** Git status via the same-machine bridge. */
export function useLocalGitStatus(proxyUrl: string | undefined) {
  return useQuery({
    queryKey: queryKeys.daemon.gitLocal(proxyUrl ?? ''),
    enabled: Boolean(proxyUrl),
    queryFn: ({ signal }) => daemonApi.gitStatus(proxyUrl!, signal),
    refetchInterval: 15_000,
    retry: false,
  })
}

/** Bind the UI project to the folder the local daemon is watching. */
export function useBindLocalWorkspace() {
  const { setProjectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (folderName: string) => {
      const name = folderName.trim()
      if (!name) throw new Error('No workspace folder detected')
      const project = await projectsApi.resolve({ folder_name: name })
      if (!project?.id) throw new Error('Could not resolve project')
      return project
    },
    onSuccess: (project) => {
      setProjectId(project.id)
      void qc.invalidateQueries({ queryKey: queryKeys.project.current })
      void qc.invalidateQueries({ queryKey: queryKeys.project.list })
      void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
      void qc.invalidateQueries({ queryKey: queryKeys.daemon.local(project.id) })
    },
  })
}

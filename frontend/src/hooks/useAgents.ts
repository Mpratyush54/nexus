import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { agentsApi, type AgentPutInput } from '@/api/agents'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

export function useAgents() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.agents.list(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: async () => (await agentsApi.list(projectId!)).items,
  })
}

export function useUpsertAgent() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ agentId, input }: { agentId: string; input: AgentPutInput }) =>
      agentsApi.put(projectId!, agentId, input),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.agents.list(projectId ?? '') })
    },
  })
}

export function useDeleteAgent() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (agentId: string) => agentsApi.remove(projectId!, agentId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.agents.list(projectId ?? '') })
    },
  })
}

export function useMCPFeed() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.agents.mcpFeed(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: async ({ signal }) => (await agentsApi.listToolCalls(projectId!, signal)).items,
    refetchInterval: 15_000,
  })
}

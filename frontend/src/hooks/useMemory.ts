import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { memoryApi } from '@/api/memory'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'
import type { MemoryItem } from '@/types/api'

export function useMemorySearch(q = '') {
  const { projectId, isAuthenticated } = useAuth()

  return useQuery({
    queryKey: queryKeys.memory.search(projectId ?? '', q),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: ({ signal }) =>
      memoryApi.search(
        {
          projectId: projectId!,
          q: q || undefined,
          limit: 50,
        },
        signal,
      ),
    select: (data) => data.items,
  })
}

export function useConfirmMemory() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => memoryApi.confirm(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
    },
  })
}

export function useRejectMemory() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => memoryApi.reject(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
    },
  })
}

export function useOptimisticMemoryPatch() {
  const qc = useQueryClient()
  return (id: string, patch: Partial<MemoryItem>) => {
    qc.setQueriesData<{ items: MemoryItem[]; count: number }>(
      { queryKey: queryKeys.memory.all },
      (old) => {
        if (!old) return old
        return {
          ...old,
          items: old.items.map((item) => (item.id === id ? { ...item, ...patch } : item)),
        }
      },
    )
  }
}

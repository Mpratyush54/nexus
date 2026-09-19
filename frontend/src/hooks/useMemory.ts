import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { memoryApi } from '@/api/memory'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'
import type { MemoryItem, MemoryUpdatePayload } from '@/types/api'

export function useMemorySearch(q = '', level = '') {
  const { projectId, isAuthenticated } = useAuth()

  return useQuery({
    queryKey: queryKeys.memory.search(projectId ?? '', q, level),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: ({ signal }) =>
      memoryApi.search(
        {
          projectId: projectId!,
          q: q || undefined,
          level: level || undefined,
          limit: 100,
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

export function useUpdateMemory() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, payload }: { id: string; payload: MemoryUpdatePayload }) =>
      memoryApi.update(id, payload),
    onSuccess: (result, vars) => {
      if (result) {
        void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
        void qc.invalidateQueries({ queryKey: queryKeys.memory.history(vars.id) })
      }
    },
  })
}

export function useMemoryHistory(id: string | null, enabled = true) {
  return useQuery({
    queryKey: queryKeys.memory.history(id ?? ''),
    enabled: enabled && Boolean(id),
    queryFn: ({ signal }) => memoryApi.history(id!, signal),
    retry: false,
  })
}

export function useRevertMemory() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, version }: { id: string; version: number }) =>
      memoryApi.revert(id, version),
    onSuccess: (result, vars) => {
      if (result) {
        void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
        void qc.invalidateQueries({ queryKey: queryKeys.memory.history(vars.id) })
      }
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

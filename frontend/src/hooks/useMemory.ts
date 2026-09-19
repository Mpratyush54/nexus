import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { memoryApi } from '@/api/memory'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'
import type { MemoryItem, MemoryUpdatePayload } from '@/types/api'

export const LIBRARY_PAGE_SIZE = 40

export function useMemorySearch(q = '', level = '') {
  const { projectId, isAuthenticated } = useAuth()
  const [page, setPage] = useState(0)

  useEffect(() => {
    setPage(0)
  }, [projectId, q, level])

  const query = useQuery({
    queryKey: [...queryKeys.memory.search(projectId ?? '', q, level), page] as const,
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: ({ signal }) =>
      memoryApi.search(
        {
          projectId: projectId!,
          q: q || undefined,
          level: level || undefined,
          limit: LIBRARY_PAGE_SIZE,
          offset: page * LIBRARY_PAGE_SIZE,
        },
        signal,
      ),
  })

  const items = query.data?.items ?? []
  const total = query.data?.total ?? query.data?.count ?? items.length
  const pageCount = Math.max(1, Math.ceil(total / LIBRARY_PAGE_SIZE))
  const hasPrev = page > 0
  const hasNext =
    query.data?.has_more === true ||
    (page + 1) * LIBRARY_PAGE_SIZE < total ||
    (query.data?.has_more == null && items.length >= LIBRARY_PAGE_SIZE)

  return {
    ...query,
    data: items,
    total,
    page,
    pageCount,
    pageSize: LIBRARY_PAGE_SIZE,
    hasPrev,
    hasNext,
    setPage,
    nextPage: () => setPage((p) => p + 1),
    prevPage: () => setPage((p) => Math.max(0, p - 1)),
  }
}

/** Raw harvest jobs (queued / processing / done) for the active project. */
export function useHarvestQueue() {
  const { projectId, isAuthenticated } = useAuth()
  const qc = useQueryClient()
  const prevResults = useRef(0)
  const prevInFlight = useRef(0)

  const query = useQuery({
    queryKey: queryKeys.memory.harvest(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: ({ signal }) => memoryApi.harvestQueue(projectId!, signal),
    select: (data) => data.items ?? [],
    refetchInterval: 4_000,
    retry: false,
  })

  useEffect(() => {
    prevResults.current = 0
    prevInFlight.current = 0
  }, [projectId])

  useEffect(() => {
    const items = query.data ?? []
    const results = items.reduce((n, j) => n + (j.result_count ?? 0), 0)
    const inFlight = items.filter((j) => j.status === 'queued' || j.status === 'processing').length
    // Refresh Library when new memories land or a batch leaves the in-flight queue.
    if (results > prevResults.current || (prevInFlight.current > 0 && inFlight < prevInFlight.current)) {
      void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
    }
    prevResults.current = results
    prevInFlight.current = inFlight
  }, [query.data, qc])

  return query
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

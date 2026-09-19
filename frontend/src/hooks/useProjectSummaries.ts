import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { dashboardApi, type DashboardMetrics } from '@/api/dashboard'
import { queryKeys } from '@/lib/query-keys'
import { useProjects } from '@/hooks/useProjects'
import { useAuth } from '@/providers/AuthProvider'
import type { Project } from '@/types/api'

export type ProjectSummary = {
  project: Project
  metrics: Pick<DashboardMetrics, 'memories' | 'proposed' | 'confirmed'>
}

/** Per-project memory counts for the Home grid + auto-select. */
export function useProjectSummaries() {
  const { isAuthenticated } = useAuth()
  const projects = useProjects()
  const list = projects.data ?? []

  return useQuery({
    queryKey: [...queryKeys.project.list, 'summaries', list.map((p) => p.id).join(',')],
    enabled: isAuthenticated && list.length > 0,
    staleTime: 15_000,
    queryFn: async ({ signal }) => {
      const rows = await Promise.all(
        list.map(async (project) => {
          try {
            const dash = await dashboardApi.get(project.id, signal)
            return {
              project,
              metrics: {
                memories: dash.metrics?.memories ?? 0,
                proposed: dash.metrics?.proposed ?? 0,
                confirmed: dash.metrics?.confirmed ?? 0,
              },
            } satisfies ProjectSummary
          } catch {
            return {
              project,
              metrics: { memories: 0, proposed: 0, confirmed: 0 },
            } satisfies ProjectSummary
          }
        }),
      )
      return rows.sort((a, b) => {
        const score = (m: ProjectSummary['metrics']) => m.proposed * 2 + m.memories + m.confirmed
        return score(b.metrics) - score(a.metrics)
      })
    },
  })
}

/**
 * If there is no active project (or it vanished), pick the richest one.
 * Does not override a valid selection — Home shows a switch banner instead.
 */
export function useEnsureActiveProject() {
  const { projectId, setProjectId, isAuthenticated } = useAuth()
  const projects = useProjects()
  const summaries = useProjectSummaries()

  useEffect(() => {
    if (!isAuthenticated) return
    const list = projects.data
    if (!list?.length) return
    const stillValid = Boolean(projectId && list.some((p) => p.id === projectId))
    if (stillValid) return
    const best = summaries.data?.[0]?.project ?? list[0]
    if (best?.id) setProjectId(best.id)
  }, [isAuthenticated, projectId, projects.data, summaries.data, setProjectId])
}

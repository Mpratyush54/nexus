import { apiRequest } from '@/lib/api-client'
import type { ProjectEvent } from '@/types/api'

export type DashboardMetrics = {
  memories: number
  proposed: number
  confirmed: number
  members: number
  online: number
  events_7d: number
  agent_calls: number
  github_connected: boolean
  github_repo?: string
}

export type HeatDay = { date: string; count: number }
export type TypeCount = { type: string; count: number }

export type ProjectDashboard = {
  project_id: string
  metrics: DashboardMetrics
  heatmap: HeatDay[]
  by_type: TypeCount[]
  recent: ProjectEvent[]
}

export const dashboardApi = {
  get(projectId: string, signal?: AbortSignal) {
    return apiRequest<ProjectDashboard>(`/projects/${projectId}/dashboard`, { signal })
  },
}

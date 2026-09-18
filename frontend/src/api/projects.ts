import { apiDownload, apiRequest } from '@/lib/api-client'
import type { Project } from '@/types/api'

export type ResolveProjectInput = {
  folder_name?: string
  canonical_url?: string
  root_commit?: string
}

export type ExportMemory = {
  key: string
  content: string
  level?: string
  scope?: string
  tags?: string[]
  status?: string
  visibility?: string
}

export type ProjectExport = {
  version: number
  exported_at: string
  project?: Project
  memories: ExportMemory[]
}

export type ImportResult = {
  project_id: string
  imported: number
  skipped: number
  errors?: string[]
}

export type ForkResult = {
  project: Project
  source_id: string
  memories_copied: number
  memories_skipped: number
  branches_copied: number
}

export const projectsApi = {
  resolve(input: ResolveProjectInput) {
    return apiRequest<Project>('/projects/resolve', {
      method: 'POST',
      body: input,
    })
  },

  exportJson(projectId: string) {
    return apiRequest<ProjectExport>(`/projects/${projectId}/export?format=json`)
  },

  async downloadExport(projectId: string, format: 'json' | 'yaml' | 'markdown') {
    const { blob, filename } = await apiDownload(`/projects/${projectId}/export?format=${format}`)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    a.click()
    URL.revokeObjectURL(url)
  },

  importMemories(projectId: string, memories: ExportMemory[]) {
    return apiRequest<ImportResult>(`/projects/${projectId}/import`, {
      method: 'POST',
      body: { memories },
    })
  },

  importMarkdown(projectId: string, markdown: string) {
    return apiRequest<ImportResult>(`/projects/${projectId}/import`, {
      method: 'POST',
      body: { markdown },
    })
  },

  fork(projectId: string, input?: { folder_name?: string; display_name?: string }) {
    return apiRequest<ForkResult>(`/projects/${projectId}/fork`, {
      method: 'POST',
      body: input ?? {},
    })
  },
}

import { apiRequest } from '@/lib/api-client'
import type { Project } from '@/types/api'

export type ResolveProjectInput = {
  folder_name?: string
  canonical_url?: string
  root_commit?: string
}

export const projectsApi = {
  resolve(input: ResolveProjectInput) {
    return apiRequest<Project>('/projects/resolve', {
      method: 'POST',
      body: input,
    })
  },
}

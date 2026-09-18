import { apiRequest } from '@/lib/api-client'
import type { ListResponse } from '@/types/api'

export type ProjectMember = {
  user_id: string
  role?: string
}

export type ProjectRole = {
  id: string
  project_id?: string
  name: string
  description?: string
  permissions?: string[]
  is_builtin?: boolean
}

export type GitHubStatus = {
  connected: boolean
  project_id: string
  owner?: string
  repo?: string
  sync_mode?: string
  connected_by?: string
  connected_at?: string
  last_import_at?: string | null
  has_token?: boolean
}

export type GitHubImportItem = {
  login: string
  github_permission: string
  mapped_role: string
  user_id?: string
  status: 'granted' | 'updated' | 'invite' | 'skipped' | string
  invite_hint?: string
  matched_by?: string
}

export type GitHubImportResult = {
  project_id: string
  owner: string
  repo: string
  imported: number
  invites: number
  count: number
  items: GitHubImportItem[]
}

export type ActiveWorkspace = {
  id?: string
  project_id?: string
  machine_id?: string
  path?: string
  user_id?: string
  branch?: string
  last_seen?: string
  is_online?: boolean
}

export const teamApi = {
  listMembers(projectId: string) {
    return apiRequest<ListResponse<ProjectMember>>(`/projects/${projectId}/members`)
  },
  grantMember(projectId: string, userId: string) {
    return apiRequest<{ project_id: string; user_id: string }>(`/projects/${projectId}/members`, {
      method: 'POST',
      body: { user_id: userId },
    })
  },
  revokeMember(projectId: string, userId: string) {
    return apiRequest<{ project_id: string; user_id: string; revoked: boolean }>(
      `/projects/${projectId}/members`,
      { method: 'DELETE', body: { user_id: userId } },
    )
  },
  setMemberRole(projectId: string, userId: string, role: string) {
    return apiRequest(`/projects/${projectId}/members/${userId}/role`, {
      method: 'PUT',
      body: { role },
    })
  },
  listRoles(projectId: string) {
    return apiRequest<ListResponse<ProjectRole>>(`/projects/${projectId}/roles`)
  },
  githubStatus(projectId: string) {
    return apiRequest<GitHubStatus>(`/projects/${projectId}/github/status`)
  },
  githubConnect(
    projectId: string,
    input: { owner: string; repo: string; access_token?: string; sync_mode?: string },
  ) {
    return apiRequest(`/projects/${projectId}/github/connect`, {
      method: 'POST',
      body: input,
    })
  },
  githubImport(projectId: string) {
    return apiRequest<GitHubImportResult>(`/projects/${projectId}/github/import`, {
      method: 'POST',
      body: {},
    })
  },
  githubDisconnect(projectId: string) {
    return apiRequest(`/projects/${projectId}/github/disconnect`, { method: 'DELETE' })
  },
  activeWorkspaces(projectId: string) {
    return apiRequest<ListResponse<ActiveWorkspace> | ActiveWorkspace | ActiveWorkspace[]>(
      `/workspaces/${projectId}/active`,
    )
  },
}

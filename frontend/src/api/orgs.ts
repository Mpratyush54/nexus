import { apiRequest } from '@/lib/api-client'
import type { ListResponse, Organization, OrganizationMember, Project } from '@/types/api'

export type OrgDetail = {
  organization: Organization
  members: OrganizationMember[]
  projects: Project[]
}

export const orgsApi = {
  list() {
    return apiRequest<ListResponse<Organization>>('/orgs')
  },
  create(input: { name: string; slug?: string }) {
    return apiRequest<Organization>('/orgs', { method: 'POST', body: input })
  },
  get(id: string) {
    return apiRequest<OrgDetail>(`/orgs/${id}`)
  },
  addMember(orgId: string, input: { user_id: string; role?: string }) {
    return apiRequest<OrganizationMember>(`/orgs/${orgId}/members`, {
      method: 'POST',
      body: input,
    })
  },
  setMemberRole(orgId: string, userId: string, role: string) {
    return apiRequest<OrganizationMember>(`/orgs/${orgId}/members/${userId}/role`, {
      method: 'PUT',
      body: { role },
    })
  },
  removeMember(orgId: string, userId: string) {
    return apiRequest(`/orgs/${orgId}/members/${userId}`, { method: 'DELETE' })
  },
  createProject(
    orgId: string,
    input: { folder_name: string; display_name?: string; canonical_url?: string },
  ) {
    return apiRequest<Project>(`/orgs/${orgId}/projects`, { method: 'POST', body: input })
  },
  storage(orgId: string) {
    return apiRequest<OrgStorageReport>(`/orgs/${orgId}/storage`)
  },
  audit(orgId: string) {
    return apiRequest<ListResponse<OrgAuditEvent>>(`/orgs/${orgId}/audit?limit=30`)
  },
  shares(orgId: string) {
    return apiRequest<ListResponse<OrgShare>>(`/orgs/${orgId}/shares`)
  },
  revokeShare(orgId: string, sessionId: string, userId: string) {
    return apiRequest<{ session_id: string; user_id: string; revoked: boolean }>(
      `/orgs/${orgId}/shares/${sessionId}/${userId}`,
      { method: 'DELETE' },
    )
  },
  offboardPreview(orgId: string, userId: string) {
    return apiRequest<OffboardPreview>(
      `/orgs/${orgId}/offboard/preview?user=${encodeURIComponent(userId)}`,
    )
  },
  offboard(orgId: string, input: { from_user_id: string; to_user_id: string }) {
    return apiRequest<OffboardResult>(`/orgs/${orgId}/offboard`, {
      method: 'POST',
      body: input,
    })
  },
  createInvite(orgId: string, input: { email: string; role?: string }) {
    return apiRequest<OrgInvite>(`/orgs/${orgId}/invites`, { method: 'POST', body: input })
  },
  setCapture(projectId: string, enabled: boolean) {
    return apiRequest<{ project_id: string; enabled: boolean }>(`/projects/${projectId}/capture`, {
      method: 'PUT',
      body: { enabled },
    })
  },
}

export type OrgStorageReport = {
  members: { user_id: string; session_count: number; last_active_at?: string }[]
  projects: {
    project_id: string
    folder_name: string
    display_name?: string
    capture_enabled: boolean
    session_count: number
    storage_bytes: number
  }[]
}

export type OrgAuditEvent = {
  at: string
  actor_user_id?: string
  action: string
  resource_kind?: string
  outcome: string
}

export type OrgShare = {
  session_id: string
  project_id: string
  owner_id: string
  grantee_id: string
  live: boolean
  created_at: string
}

export type OffboardPreview = {
  sessions: number
  grants: number
  tokens: number
}

export type OffboardResult = {
  sessions_transferred: number
  grants_kept: number
  secret_grants_transferred?: number
  tokens_revoked?: number
}

export type OrgInvite = {
  id: string
  email: string
  role: string
  token?: string
}

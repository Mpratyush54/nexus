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
}

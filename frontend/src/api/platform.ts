import { apiRequest } from '@/lib/api-client'
import type { ListResponse } from '@/types/api'

export type PlatformAppInfo = {
  app: string
  version: string
  commit?: string
  built_at?: string
}

export type PlatformVersion = {
  api: PlatformAppInfo
  apps: PlatformAppInfo[]
}

export type ReleaseArtifact = {
  os: string
  arch: string
  url: string
  filename?: string
  sha256?: string
  size?: number
}

export type AppRelease = {
  id: string
  app: string
  version: string
  channel: string
  notes?: string
  git_sha?: string
  artifacts?: ReleaseArtifact[]
  published_by?: string
  published_at: string
  yanked?: boolean
}

export type AdminOverview = {
  role: string
  stats: { users: number; orgs: number; projects: number }
  api: PlatformAppInfo
  aws: {
    region?: string
    ecs_cluster?: string
    ecs_service?: string
    releases_bucket?: string
  }
  bootstrap_env?: string
}

export type AdminUser = {
  id: string
  username: string
  email?: string
  is_platform_admin: boolean
  source?: string
}

export const platformApi = {
  version(signal?: AbortSignal) {
    return apiRequest<PlatformVersion>('/platform/version', { signal, auth: false })
  },
  latest(app = 'cli', channel = 'stable') {
    const q = new URLSearchParams({ app, channel })
    return apiRequest<AppRelease>(`/platform/releases/latest?${q}`, { auth: false })
  },
  overview() {
    return apiRequest<AdminOverview>('/admin/overview')
  },
  users() {
    return apiRequest<ListResponse<AdminUser>>('/admin/users')
  },
  setAdmin(id: string, is_platform_admin: boolean) {
    return apiRequest<{ id: string; is_platform_admin: boolean }>(`/admin/users/${id}`, {
      method: 'PUT',
      body: { is_platform_admin },
    })
  },
  releases(app?: string) {
    const q = app ? `?app=${encodeURIComponent(app)}` : ''
    return apiRequest<ListResponse<AppRelease>>(`/admin/releases${q}`)
  },
  publish(rel: {
    app: string
    version: string
    channel?: string
    notes?: string
    git_sha?: string
    artifacts?: ReleaseArtifact[]
  }) {
    return apiRequest<AppRelease>('/admin/releases', { method: 'POST', body: rel })
  },
  yank(app: string, version: string) {
    return apiRequest(`/admin/releases/${app}/${version}/yank`, { method: 'POST', body: {} })
  },
}

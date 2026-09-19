import { apiRequest } from '@/lib/api-client'
import type { LoginResponse } from '@/types/api'

export type LoginInput = {
  username: string
  password: string
}

export type SignupInput = {
  username: string
  password: string
  email: string
}

export type UserProfile = {
  id: string
  username: string
  email?: string
  settings?: Record<string, unknown>
  created_at: string
  is_platform_admin?: boolean
}

export type UsageStats = {
  memories_created: number
  episodes_created: number
  requests_approx: number
}

export type ApiToken = {
  id: string
  user_id: string
  name: string
  token_prefix: string
  scopes: string[]
  agent_id?: string
  created_at: string
  last_used_at?: string
}

export type CreateTokenInput = {
  name: string
  scopes?: string[]
  agent_id?: string
}

export const authApi = {
  login(input: LoginInput) {
    return apiRequest<LoginResponse>('/auth/login', {
      method: 'POST',
      body: input,
      auth: false,
    })
  },

  signup(input: SignupInput) {
    return apiRequest<LoginResponse>('/auth/signup', {
      method: 'POST',
      body: input,
      auth: false,
    })
  },

  me() {
    return apiRequest<UserProfile>('/users/me')
  },

  updateMe(body: { email?: string; settings?: Record<string, unknown> }) {
    return apiRequest<UserProfile>('/users/me', { method: 'PUT', body })
  },

  changePassword(body: { current_password: string; new_password: string }) {
    return apiRequest<{ ok: boolean }>('/users/me/password', {
      method: 'PUT',
      body,
    })
  },

  usage() {
    return apiRequest<UsageStats>('/users/me/usage')
  },

  listTokens() {
    return apiRequest<{ items: ApiToken[]; count: number }>('/auth/tokens')
  },

  createToken(body: CreateTokenInput) {
    return apiRequest<{
      token: string
      id: string
      name: string
      prefix: string
      scopes: string[]
      agent_id?: string
      created_at: string
    }>('/auth/tokens', { method: 'POST', body })
  },

  revokeToken(tokenId: string) {
    return apiRequest<{ id: string; revoked: boolean }>(`/auth/tokens/${tokenId}`, {
      method: 'DELETE',
    })
  },
}

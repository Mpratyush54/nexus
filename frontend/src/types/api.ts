export type ApiErrorBody = {
  error: {
    code: number
    message: string
  }
}

export class ApiError extends Error {
  status: number
  code: number

  constructor(status: number, message: string, code?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code ?? status
  }
}

export type LoginResponse = {
  token: string
  username: string
  user_id: string
}

export type MemoryItem = {
  id: string
  project_id?: string
  user_id?: string
  session_id?: string
  org_id?: string
  key: string
  content: string
  context_snippet?: string
  level: string
  scope: string
  tags?: string[]
  confidence: number
  status: 'PROPOSED' | 'CONFIRMED' | 'REJECTED' | 'SUPERSEDED' | string
  source?: string
  proposed_by?: string
  confirmed_by?: string
  use_count: number
  created_at: string
  updated_at: string
}

/** Partial update body for PUT /memory/{id}. */
export type MemoryUpdatePayload = {
  key?: string
  content?: string
  tags?: string[]
  level?: string
  scope?: string
  context_snippet?: string
}

export type MemoryVersion = {
  id?: string | number
  memory_id?: string
  version: number
  key: string
  content: string
  tags?: string[]
  level?: string
  scope?: string
  edited_by?: string
  created_at: string
}

export type MemoryShareVisibility = 'private' | 'shared' | 'project' | 'public'

export type ListResponse<T> = {
  items: T[]
  count: number
}

export type Project = {
  id: string
  folder_name?: string
  canonical_url?: string
  root_commit?: string
  created_by?: string
}

export type WsEnvelope = {
  type: string
  event_type?: string
  user_id?: string
  project_id?: string
  session_id?: string
  payload?: unknown
  item?: MemoryItem
  action?: 'proposed' | 'confirmed' | 'rejected' | string
  status?: 'online' | 'typing' | 'idle' | 'offline' | string
  message?: string
}

import { API_BASE } from '@/lib/env'
import { ApiError, type ApiErrorBody } from '@/types/api'
import { storage } from '@/utils/storage'

type RequestOpts = {
  method?: string
  body?: unknown
  token?: string | null
  auth?: boolean
  signal?: AbortSignal
}

export async function apiRequest<T>(path: string, opts: RequestOpts = {}): Promise<T> {
  const { method = 'GET', body, auth = true, signal } = opts
  const headers: Record<string, string> = {
    Accept: 'application/json',
  }

  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }

  const token = opts.token === undefined ? storage.getToken() : opts.token
  if (auth) {
    if (!token) throw new ApiError(401, 'Not authenticated')
    headers.Authorization = `Bearer ${token}`
  }

  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  })

  if (res.status === 204) {
    return undefined as T
  }

  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      throw new ApiError(res.status, text || res.statusText)
    }
  }

  if (!res.ok) {
    const err = data as ApiErrorBody | null
    const message = err?.error?.message || res.statusText || 'Request failed'
    if (res.status === 401 && auth) {
      storage.clearSession()
    }
    throw new ApiError(res.status, message, err?.error?.code)
  }

  return data as T
}

import { API_BASE } from '@/lib/env'
import { breadcrumb, rememberErrorRef } from '@/lib/diagnostics'
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

  const started = Date.now()
  let res: Response
  try {
    res = await fetch(`${API_BASE}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    })
  } catch (err) {
    if (!(err instanceof DOMException && err.name === 'AbortError')) breadcrumb(`${method} ${path} -> network error`)
    throw err
  }
  const requestId = res.headers.get('X-Request-ID') ?? ''
  breadcrumb(`${method} ${path} -> ${res.status} ${Date.now() - started}ms${res.ok ? '' : ` ref=${requestId}`}`)

  if (res.status === 204) {
    return undefined as T
  }

  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      const message = text || res.statusText
      if (!res.ok) rememberErrorRef(message, requestId)
      throw new ApiError(res.status, message, undefined, requestId)
    }
  }

  if (!res.ok) {
    const err = data as ApiErrorBody | null
    const message = err?.error?.message || res.statusText || 'Request failed'
    const id = err?.error?.request_id || requestId
    if (res.status === 401 && auth) {
      storage.clearSession()
    }
    rememberErrorRef(message, id)
    throw new ApiError(res.status, message, err?.error?.code, id)
  }

  return data as T
}

export async function apiDownload(
  path: string,
  opts: { token?: string | null; auth?: boolean } = {},
): Promise<{ blob: Blob; filename: string; contentType: string }> {
  const headers: Record<string, string> = { Accept: '*/*' }
  const token = opts.token === undefined ? storage.getToken() : opts.token
  const auth = opts.auth !== false
  if (auth) {
    if (!token) throw new ApiError(401, 'Not authenticated')
    headers.Authorization = `Bearer ${token}`
  }
  const res = await fetch(`${API_BASE}${path}`, { method: 'GET', headers })
  if (!res.ok) {
    const text = await res.text()
    let message = res.statusText || 'Request failed'
    let id = res.headers.get('X-Request-ID') ?? ''
    try {
      const err = JSON.parse(text) as ApiErrorBody
      if (err?.error?.message) message = err.error.message
      if (err?.error?.request_id) id = err.error.request_id
    } catch {
      if (text) message = text
    }
    breadcrumb(`GET ${path} -> ${res.status} ref=${id}`)
    rememberErrorRef(message, id)
    throw new ApiError(res.status, message, undefined, id)
  }
  const blob = await res.blob()
  const cd = res.headers.get('Content-Disposition') ?? ''
  const match = /filename="?([^"]+)"?/i.exec(cd)
  return {
    blob,
    filename: match?.[1] ?? 'nexus-export',
    contentType: res.headers.get('Content-Type') ?? blob.type,
  }
}

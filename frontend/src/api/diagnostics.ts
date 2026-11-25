import { apiRequest } from '@/lib/api-client'

/** One fingerprint's summary (GET /admin/diagnostics). */
export type ProblemGroup = {
  fingerprint: string
  kind: 'crash' | 'error' | 'user_report'
  source: 'desktop' | 'core' | 'web' | 'cli'
  message: string
  count: number
  users: number
  first_seen: string
  last_seen: string
  versions: string[]
  latest_report_id: string
}

/** One stored report (GET /admin/diagnostics/{fingerprint}). */
export type ProblemReport = {
  report_id: string
  fingerprint: string
  kind: ProblemGroup['kind']
  source: ProblemGroup['source']
  app_version: string
  os: string
  message: string
  stack?: string
  logs?: string[]
  user_note?: string
  context?: Record<string, string>
  user_id?: string
  request_id?: string
  timestamp: string
  received_at: string
}

export type ProblemFilter = { source?: string; kind?: string }

export const diagnosticsApi = {
  groups: (f: ProblemFilter = {}) => {
    const q = new URLSearchParams()
    if (f.source) q.set('source', f.source)
    if (f.kind) q.set('kind', f.kind)
    const qs = q.toString()
    return apiRequest<{ items: ProblemGroup[]; count: number; retention_days?: number }>(`/admin/diagnostics${qs ? `?${qs}` : ''}`)
  },
  group: (fingerprint: string) =>
    apiRequest<{ fingerprint: string; items: ProblemReport[]; count: number }>(`/admin/diagnostics/${encodeURIComponent(fingerprint)}`),
}

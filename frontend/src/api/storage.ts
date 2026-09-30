import { apiRequest } from '@/lib/api-client'

export type StorageUsage = {
  scope: string
  used: number
  cap: number
  state: 'ok' | 'warn' | 'full' | string
}

export const storageApi = {
  usage(scope?: string, signal?: AbortSignal) {
    const q = scope ? `?scope=${encodeURIComponent(scope)}` : ''
    return apiRequest<StorageUsage>(`/v1/storage/usage${q}`, { signal })
  },
}

export function formatBytes(n: number) {
  if (!Number.isFinite(n) || n < 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

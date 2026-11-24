export const API_BASE = import.meta.env.VITE_API_BASE ?? '/api'

export function wsUrl(token: string): string {
  const configured = import.meta.env.VITE_WS_URL as string | undefined
  if (configured) {
    const u = new URL(configured)
    u.searchParams.set('token', token)
    return u.toString()
  }
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws?token=${encodeURIComponent(token)}`
}

/**
 * Browser → desktop requests (/v1/desktop/*) need the server's desktop
 * execution queue. Until a deployment has it, leave this unset so the Chats
 * reader stays read-only and never probes routes that would 404.
 */
export const DESKTOP_RELAY = import.meta.env.VITE_DESKTOP_RELAY === '1'

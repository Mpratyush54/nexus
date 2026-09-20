import { useMemo } from 'react'
import {
  useLocalDaemonAutodetect,
  useLocalWorkspaceView,
} from '@/hooks/useDaemon'
import { useAuth } from '@/providers/AuthProvider'

export type DesktopIdentity =
  | 'no_desktop'
  | 'unsigned'
  | 'match'
  | 'mismatch'
  | 'unknown'

/**
 * Same-machine daemon bridge is only trusted when Desktop is signed in as
 * the same Nexus user as the web portal. Prevents account-A desktop + account-B
 * portal from reading harvest / switching projects / opening local files.
 */
export function useTrustedLocalBridge() {
  const { user } = useAuth()
  const local = useLocalDaemonAutodetect()
  const serverView = useLocalWorkspaceView()

  const webUserId = (user?.userId ?? '').trim()
  const desktopUserId = (local.data?.status.user_id ?? '').trim()
  const desktopUsername = (local.data?.status.username ?? '').trim()
  const desktopOnline = Boolean(local.data)

  const identity: DesktopIdentity = useMemo(() => {
    if (!desktopOnline) return 'no_desktop'
    if (!desktopUserId) {
      // Older daemons / healthz-only probe — treat as unknown, do not trust.
      if (!local.data?.status.has_token && !local.data?.status.connected) {
        return 'unsigned'
      }
      return 'unknown'
    }
    if (!webUserId) return 'unknown'
    return desktopUserId === webUserId ? 'match' : 'mismatch'
  }, [desktopOnline, desktopUserId, webUserId, local.data?.status.has_token, local.data?.status.connected])

  const trusted = identity === 'match'

  const rawBridge =
    local.data?.baseUrl ||
    local.data?.status.proxy_url ||
    (trusted ? serverView.data?.proxy_url : undefined) ||
    undefined

  /** Only pass to harvest / scan / file APIs when identities match. */
  const bridgeUrl = trusted ? rawBridge : undefined

  return {
    local,
    serverView,
    identity,
    trusted,
    bridgeUrl,
    desktopUserId: desktopUserId || null,
    desktopUsername: desktopUsername || null,
    webUserId: webUserId || null,
    webUsername: user?.username ?? null,
  }
}

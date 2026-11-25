import { useEffect, useSyncExternalStore } from 'react'

/**
 * Hand-off from the sign-in screens into the app.
 *
 * - 'holding': a sign-in request is in flight. The session is stored before
 *   the request resolves, and GuestRoute would otherwise swap the page out
 *   mid-request; while holding it keeps the form mounted so the page can show
 *   its own result and choose where to go.
 * - 'covering': signed in. A full-screen cover fades in over the form, the
 *   app route loads under it, and the cover fades out once the app has drawn.
 */
export type HandoffPhase = 'idle' | 'holding' | 'covering'

let phase: HandoffPhase = 'idle'
const listeners = new Set<() => void>()

function set(next: HandoffPhase) {
  if (phase === next) return
  phase = next
  listeners.forEach((l) => l())
}

export const handoff = {
  hold: () => set('holding'),
  cover: () => set('covering'),
  release: () => set('idle'),
  get: () => phase,
}

export function useHandoff(): HandoffPhase {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => phase,
    () => 'idle',
  )
}

/** Starts downloading the first app screen while the sign-in request runs. */
export function prefetchApp() {
  void import('@/pages/DashboardPage').catch(() => {})
}

/** Where to go after signing in: a safe in-app `next`, or Home. */
export function afterAuthPath(next: string | null) {
  return next && (next.startsWith('/cli/') || next.startsWith('/app/')) ? next : '/app/dashboard'
}

/**
 * Where a signed-in visit to /login or /signup goes: where a sign-in would
 * have gone (a safe `next`, else Home). Desktop setup is offered from Home,
 * never forced. Null while a sign-in on the page is handing off.
 */
export function guestRedirect(isAuthenticated: boolean, handoff: HandoffPhase, next: string | null): string | null {
  if (!isAuthenticated || handoff !== 'idle') return null
  return afterAuthPath(next)
}

/** A page that started a sign-in and is left mid-request must not keep GuestRoute on hold. */
export function useReleaseHoldOnLeave() {
  useEffect(
    () => () => {
      if (phase === 'holding') set('idle')
    },
    [],
  )
}

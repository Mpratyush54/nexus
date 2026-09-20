import { useEffect, useRef } from 'react'
import { useAuth } from '@/providers/AuthProvider'
import type { HarvestStatus } from '@/api/daemon'

/**
 * When the local daemon targets a different project than the one selected in
 * the portal (common when folder is central-memory but git remote resolves to
 * nexus), switch so Memory/Dashboard show the rows that are actually uploaded.
 *
 * Only runs when `allowed` is true (Desktop signed in as the same user as the
 * web session) — otherwise a second account on this machine could yank the
 * portal into someone else's harvest project.
 */
export function useFollowHarvestProject(
  hs: HarvestStatus | undefined,
  allowed = false,
) {
  const { projectId, setProjectId } = useAuth()
  const switchedFor = useRef<string | null>(null)

  useEffect(() => {
    if (!allowed) return
    const target = hs?.project_id?.trim()
    if (!target) return
    if (projectId === target) {
      switchedFor.current = target
      return
    }
    // Only auto-follow once per harvest target to avoid fighting a manual switch.
    if (switchedFor.current === target) return
    switchedFor.current = target
    setProjectId(target)
  }, [allowed, hs?.project_id, projectId, setProjectId])
}

import { useEffect, useRef } from 'react'
import { useAuth } from '@/providers/AuthProvider'
import type { HarvestStatus } from '@/api/daemon'

/**
 * When the local daemon targets a different project than the one selected in
 * the portal (common when folder is central-memory but git remote resolves to
 * nexus), switch so Memory/Dashboard show the rows that are actually uploaded.
 */
export function useFollowHarvestProject(hs: HarvestStatus | undefined) {
  const { projectId, setProjectId } = useAuth()
  const switchedFor = useRef<string | null>(null)

  useEffect(() => {
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
  }, [hs?.project_id, projectId, setProjectId])
}

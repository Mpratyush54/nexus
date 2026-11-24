import { useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import { chatsApi } from '@/api/chats'
import { useAuth } from '@/providers/AuthProvider'

/**
 * Display names for user IDs in the active project: you, then the people the
 * server lists as share recipients (project members and org colleagues).
 * Unknown IDs fall back to a short form instead of a full UUID.
 */
export function usePeople() {
  const { projectId, user } = useAuth()
  const recipients = useQuery({
    queryKey: ['share-recipients', projectId ?? ''],
    enabled: Boolean(projectId),
    staleTime: 60_000,
    queryFn: () => chatsApi.recipients(projectId!),
  })
  const people = recipients.data?.people ?? recipients.data?.items ?? []
  const nameFor = useCallback(
    (id: string | undefined | null, opts: { you?: boolean } = {}) => {
      const uid = (id ?? '').trim()
      if (!uid) return 'Someone'
      if (uid === user?.userId) return opts.you === false ? user.username : 'You'
      const match = people.find((p) => p.user_id === uid)
      if (match?.name) return match.name
      return /^[0-9a-f-]{20,}$/i.test(uid) ? `User ${uid.slice(0, 6)}` : uid
    },
    [people, user],
  )
  return { nameFor, people, isLoading: recipients.isLoading }
}

export function initialsOf(name: string) {
  const parts = name.replace(/^User\s+/, '').trim().split(/[\s._-]+/).filter(Boolean)
  const letters = parts.length > 1 ? parts[0][0] + parts[1][0] : (parts[0] ?? '?').slice(0, 2)
  return letters.toUpperCase()
}

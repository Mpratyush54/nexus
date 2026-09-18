import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { sessionsApi } from '@/api/sessions'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

export function useSessions(activeOnly = false) {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.sessions.list(projectId ?? '', activeOnly),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: async () => (await sessionsApi.list(projectId!, activeOnly)).items,
  })
}

export function useCreateSession() {
  const { projectId } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (title: string) => sessionsApi.create(projectId!, title),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['sessions'] })
    },
  })
}

export function useJoinSession() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => sessionsApi.join(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['sessions'] })
    },
  })
}

export function useLeaveSession() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => sessionsApi.leave(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['sessions'] })
    },
  })
}

export function useHandoff() {
  return useMutation({
    mutationFn: (input: { sessionId: string; toUser: string; note?: string }) =>
      sessionsApi.handoff(input.sessionId, {
        to_user: input.toUser,
        note: input.note,
      }),
  })
}

export function useAcceptHandoff() {
  return useMutation({
    mutationFn: (input: { sessionId: string; handoffId: string }) =>
      sessionsApi.acceptHandoff(input.sessionId, input.handoffId),
  })
}

export function useSteerInterrupt() {
  return useMutation({
    mutationFn: (input: { sessionId: string; reason?: string }) =>
      sessionsApi.steerInterrupt(input.sessionId, input.reason),
  })
}

export function useSteerPrompt() {
  return useMutation({
    mutationFn: (input: { sessionId: string; prompt: string }) =>
      sessionsApi.steerPrompt(input.sessionId, input.prompt),
  })
}

export function useSteerResume() {
  return useMutation({
    mutationFn: (sessionId: string) => sessionsApi.steerResume(sessionId),
  })
}

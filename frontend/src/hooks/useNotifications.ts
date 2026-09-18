import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { notificationsApi } from '@/api/notifications'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

export function useNotifications() {
  const { projectId, isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.notifications.list(projectId ?? ''),
    enabled: isAuthenticated && Boolean(projectId),
    queryFn: async () => (await notificationsApi.list(projectId!)).items,
    refetchInterval: 30_000,
  })
}

export function useNotificationDevices() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.notifications.devices,
    enabled: isAuthenticated,
    queryFn: async () => (await notificationsApi.devices()).items,
  })
}

export function useNotificationPrefs() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.notifications.prefs,
    enabled: isAuthenticated,
    queryFn: () => notificationsApi.preferences(),
  })
}

export function useSaveNotificationPrefs() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (types: Record<string, boolean>) => notificationsApi.savePreferences(types),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.notifications.prefs })
      void qc.invalidateQueries({ queryKey: ['notifications'] })
    },
  })
}

export function useRevokeDevice() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => notificationsApi.revokeDevice(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.notifications.devices })
    },
  })
}

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { billingApi } from '@/api/billing'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'

export function useBillingPlans() {
  return useQuery({
    queryKey: queryKeys.billing.plans,
    queryFn: async () => (await billingApi.plans()).items,
  })
}

export function useMyBilling() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.billing.me,
    enabled: isAuthenticated,
    queryFn: () => billingApi.me(),
  })
}

export function useSetMyPlan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (planId: string) => billingApi.setMine(planId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.billing.me })
      void qc.invalidateQueries({ queryKey: queryKeys.billing.admin })
    },
  })
}

export function useOrgBilling(orgId: string | null) {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.billing.org(orgId ?? ''),
    enabled: isAuthenticated && Boolean(orgId),
    queryFn: () => billingApi.org(orgId!),
  })
}

export function useSetOrgPlan(orgId: string | null) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (planId: string) => billingApi.setOrg(orgId!, planId),
    onSuccess: () => {
      if (orgId) void qc.invalidateQueries({ queryKey: queryKeys.billing.org(orgId) })
      void qc.invalidateQueries({ queryKey: queryKeys.billing.admin })
    },
  })
}

export function useAdminSubscriptions() {
  const { isAuthenticated } = useAuth()
  return useQuery({
    queryKey: queryKeys.billing.admin,
    enabled: isAuthenticated,
    queryFn: async () => (await billingApi.adminList()).items,
    retry: false,
  })
}

export function useAdminAssignPlan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { owner_type: string; owner_id: string; plan_id: string }) =>
      billingApi.adminAssign(input),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.billing.admin })
      void qc.invalidateQueries({ queryKey: queryKeys.billing.me })
    },
  })
}

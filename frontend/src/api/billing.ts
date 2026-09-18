import { apiRequest } from '@/lib/api-client'
import type { ListResponse } from '@/types/api'

export type PlanLimits = {
  orgs: number
  projects: number
  members: number
  memories: number
  github_import: boolean
}

export type BillingPlan = {
  id: string
  name: string
  description?: string
  price_cents: number
  currency: string
  interval: string
  public: boolean
  rank: number
  limits: PlanLimits
  features?: string[]
}

export type BillingSubscription = {
  id: string
  owner_type: 'user' | 'org' | string
  owner_id: string
  plan_id: string
  status: string
  provider: string
  provider_ref?: string
  current_period_start: string
  current_period_end: string
  cancel_at_period_end?: boolean
  created_at: string
  updated_at: string
}

export type BillingUsage = {
  orgs: number
  projects: number
  members: number
  memories: number
}

export type BillingSnapshot = {
  subscription: BillingSubscription
  plan: BillingPlan
  usage: BillingUsage
  checkout: { provider: string; url?: string | null }
}

export const billingApi = {
  plans(signal?: AbortSignal) {
    return apiRequest<ListResponse<BillingPlan>>('/billing/plans', { signal, auth: false })
  },
  me() {
    return apiRequest<BillingSnapshot>('/billing/subscription')
  },
  setMine(plan_id: string) {
    return apiRequest<BillingSnapshot>('/billing/subscription', { method: 'POST', body: { plan_id } })
  },
  org(orgId: string) {
    return apiRequest<BillingSnapshot>(`/orgs/${orgId}/billing`)
  },
  setOrg(orgId: string, plan_id: string) {
    return apiRequest<BillingSnapshot>(`/orgs/${orgId}/billing`, { method: 'POST', body: { plan_id } })
  },
  adminList() {
    return apiRequest<ListResponse<BillingSubscription>>('/admin/subscriptions')
  },
  adminAssign(input: { owner_type: string; owner_id: string; plan_id: string }) {
    return apiRequest<BillingSnapshot>('/admin/subscriptions', { method: 'PUT', body: input })
  },
}

export function formatPlanPrice(plan: BillingPlan) {
  if (!plan.price_cents) return 'Free'
  const amount = (plan.price_cents / 100).toLocaleString(undefined, {
    style: 'currency',
    currency: (plan.currency || 'usd').toUpperCase(),
    maximumFractionDigits: 0,
  })
  return `${amount}/${plan.interval === 'year' ? 'yr' : 'mo'}`
}

export function formatLimit(n: number) {
  return n <= 0 ? 'Unlimited' : String(n)
}

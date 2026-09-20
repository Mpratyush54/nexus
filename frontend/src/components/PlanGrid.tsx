import { Button } from '@/components/ui/Button'
import { StatusPill } from '@/components/ui/StatusPill'
import {
  formatLimit,
  formatPlanPrice,
  type BillingPlan,
  type BillingSnapshot,
} from '@/api/billing'

type Props = {
  plans: BillingPlan[]
  current?: BillingSnapshot | null
  onSelect?: (planId: string) => void
  /** Fired when someone clicks a paid plan while upgrades are closed. */
  onPaidUnavailable?: (plan: BillingPlan) => void
  pending?: boolean
  /** When false, hide switch/upgrade actions entirely (except paid “coming soon” CTA). */
  canChange?: boolean
  /**
   * When false (default), paid plans cannot be self-selected — checkout is not wired.
   * Free ↔ free switches still allowed if canChange is true.
   */
  allowPaidUpgrade?: boolean
  highlight?: string
}

function usageLine(label: string, used: number, cap: number) {
  return `${label} ${used}/${cap <= 0 ? '∞' : cap}`
}

export function PlanGrid({
  plans,
  current,
  onSelect,
  onPaidUnavailable,
  pending,
  canChange = true,
  allowPaidUpgrade = false,
  highlight,
}: Props) {
  const activeId = current?.plan.id ?? highlight

  return (
    <div className="grid gap-3 md:grid-cols-3">
      {plans.map((plan) => {
        const active = plan.id === activeId
        const paid = plan.price_cents > 0
        const canSelect = Boolean(canChange && onSelect && !active)
        const blockedPaid = paid && !allowPaidUpgrade && !active

        return (
          <div
            key={plan.id}
            className={[
              'flex flex-col rounded-xl border p-4',
              active ? 'border-amber bg-raised' : 'border-border bg-raised/40',
            ].join(' ')}
          >
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-medium text-fg">{plan.name}</h3>
              {active ? <StatusPill tone="teal">current</StatusPill> : null}
              {blockedPaid ? <StatusPill tone="neutral">soon</StatusPill> : null}
            </div>
            <p className="mt-1 text-lg font-semibold tracking-tight text-fg">{formatPlanPrice(plan)}</p>
            {plan.description ? <p className="mt-1 text-xs text-fg-dim">{plan.description}</p> : null}
            <ul className="mt-3 space-y-1 text-xs text-fg-dim">
              {(plan.features ?? []).map((f) => (
                <li key={f}>· {f}</li>
              ))}
              <li>
                · {formatLimit(plan.limits.orgs)} orgs · {formatLimit(plan.limits.projects)} projects ·{' '}
                {formatLimit(plan.limits.members)} seats
              </li>
            </ul>
            {active && current ? (
              <p className="mt-3 text-[11px] text-muted">
                {usageLine('Orgs', current.usage.orgs, plan.limits.orgs)} ·{' '}
                {usageLine('projects', current.usage.projects, plan.limits.projects)} ·{' '}
                {usageLine('seats', current.usage.members, plan.limits.members)}
              </p>
            ) : null}

            {blockedPaid ? (
              <div className="mt-4 space-y-2">
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  className="w-full"
                  onClick={() => onPaidUnavailable?.(plan)}
                >
                  Get {plan.name}
                </Button>
                <p className="text-[11px] leading-relaxed text-muted">
                  Not available yet — keep enjoying free until paid plans open.
                </p>
              </div>
            ) : canSelect ? (
              <Button
                type="button"
                size="sm"
                className="mt-4"
                disabled={pending}
                onClick={() => onSelect?.(plan.id)}
              >
                {plan.price_cents > (current?.plan.price_cents ?? 0) ? 'Upgrade' : 'Switch'}
              </Button>
            ) : (
              <div className="mt-4" />
            )}
          </div>
        )
      })}
    </div>
  )
}

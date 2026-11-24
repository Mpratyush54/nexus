import { ChevronRight } from 'lucide-react'
import type { ReactNode } from 'react'

/**
 * Progressive disclosure for power-user controls: closed by default, one
 * plain-language line saying what is inside, the real controls underneath.
 */
export function Advanced({
  title = 'Advanced',
  summary,
  defaultOpen = false,
  id,
  children,
}: {
  title?: string
  summary?: string
  defaultOpen?: boolean
  id?: string
  children: ReactNode
}) {
  return (
    <details id={id} className="nx-advanced group rounded-xl border border-border bg-surface/60" open={defaultOpen}>
      <summary className="flex cursor-pointer list-none items-start gap-2 px-4 py-3 text-left [&::-webkit-details-marker]:hidden">
        <ChevronRight size={15} className="mt-0.5 shrink-0 text-muted transition-transform group-open:rotate-90" aria-hidden />
        <span className="min-w-0">
          <span className="block text-sm font-medium text-fg">{title}</span>
          {summary ? <span className="mt-0.5 block text-xs text-fg-dim">{summary}</span> : null}
        </span>
      </summary>
      <div className="border-t border-border px-4 py-5">{children}</div>
    </details>
  )
}

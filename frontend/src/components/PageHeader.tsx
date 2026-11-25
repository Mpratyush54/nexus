import type { ReactNode } from 'react'

/** One header shape for every page: a plain name and a one-line "what is this". */
export function PageHeader({
  title,
  description,
  actions,
  children,
}: {
  title: string
  description: ReactNode
  actions?: ReactNode
  children?: ReactNode
}) {
  return (
    <header className="space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-semibold tracking-tight text-fg">{title}</h1>
          <p className="mt-1 max-w-2xl text-sm text-fg-dim">{description}</p>
        </div>
        {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
      </div>
      {children}
    </header>
  )
}

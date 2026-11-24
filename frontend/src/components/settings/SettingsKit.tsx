import { useEffect, useRef, useState, type ReactNode } from 'react'
import { GlassPanel } from '@/components/ui/GlassPanel'

export type SectionLink = { id: string; label: string }

/**
 * One settings section: heading and summary on top, then a panel of rows.
 * `scroll-mt` keeps the heading clear of the mobile top bar and section nav
 * when it is reached from the nav.
 */
export function SettingsSection({
  id,
  title,
  description,
  children,
  tone = 'default',
}: {
  id: string
  title: string
  description?: ReactNode
  children: ReactNode
  tone?: 'default' | 'danger'
}) {
  return (
    <section id={id} aria-labelledby={`${id}-title`} className="scroll-mt-28 lg:scroll-mt-8">
      <header className="mb-3">
        <h2 id={`${id}-title`} className="text-base font-semibold tracking-tight text-fg">
          {title}
        </h2>
        {description ? <p className="mt-1 text-sm text-fg-dim">{description}</p> : null}
      </header>
      <GlassPanel className={['divide-y divide-border', tone === 'danger' ? 'border-danger/30' : ''].join(' ')}>
        {children}
      </GlassPanel>
    </section>
  )
}

/**
 * A labelled row: label and help on the left (stacked on small screens), the
 * control on the right. Pass `htmlFor` when the control is a single input.
 */
export function FieldRow({
  label,
  htmlFor,
  help,
  children,
  id,
}: {
  label: string
  htmlFor?: string
  help?: ReactNode
  children: ReactNode
  id?: string
}) {
  const helpId = id ? `${id}-help` : undefined
  return (
    <div className="grid gap-3 p-5 md:grid-cols-[minmax(0,13rem)_minmax(0,1fr)] md:gap-6">
      <div className="min-w-0">
        {htmlFor ? (
          <label htmlFor={htmlFor} className="block text-sm font-medium text-fg">
            {label}
          </label>
        ) : (
          <p id={id ? `${id}-label` : undefined} className="text-sm font-medium text-fg">
            {label}
          </p>
        )}
        {help ? (
          <p id={helpId} className="mt-1 text-xs leading-relaxed text-fg-dim">
            {help}
          </p>
        ) : null}
      </div>
      <div className="min-w-0">{children}</div>
    </div>
  )
}

/** Inline result line under a form: success (polite) or error (alert). */
export function FormMessage({ kind, children }: { kind: 'ok' | 'error'; children: ReactNode }) {
  return kind === 'error' ? (
    <p role="alert" className="mt-2 text-xs text-danger">
      {children}
    </p>
  ) : (
    <p role="status" className="mt-2 text-xs text-teal">
      {children}
    </p>
  )
}

/** Placeholder bars while a row's data loads. */
export function RowSkeleton({ lines = 1 }: { lines?: number }) {
  return (
    <div className="space-y-2" aria-hidden>
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} className={`h-4 animate-pulse rounded bg-raised ${i % 2 ? 'w-1/2' : 'w-3/4'}`} />
      ))}
    </div>
  )
}

/** Error line with a retry button, for a query that failed. */
export function LoadError({ what, onRetry }: { what: string; onRetry: () => void }) {
  return (
    <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-danger">
      <span>Couldn’t load {what}.</span>
      <button type="button" onClick={onRetry} className="text-fg underline decoration-border underline-offset-2 hover:decoration-fg">
        Try again
      </button>
    </div>
  )
}

/**
 * Section navigation: a sticky vertical list beside the content on large
 * screens, a sticky horizontally scrolling row under the top bar on smaller ones.
 */
export function SettingsNav({ sections, active }: { sections: SectionLink[]; active: string }) {
  const link = (s: SectionLink, layout: 'row' | 'col') => {
    const on = s.id === active
    return (
      <a
        key={s.id}
        href={`#${s.id}`}
        aria-current={on ? 'location' : undefined}
        className={[
          'shrink-0 rounded-lg text-sm transition-colors',
          layout === 'col' ? 'block px-3 py-1.5' : 'px-3 py-1.5',
          on ? 'bg-raised font-medium text-fg' : 'text-fg-dim hover:bg-raised/60 hover:text-fg',
        ].join(' ')}
      >
        {s.label}
      </a>
    )
  }
  const row = useRef<HTMLDivElement>(null)
  const [atEnd, setAtEnd] = useState(false)
  const onRowScroll = () => {
    const el = row.current
    if (el) setAtEnd(el.scrollLeft + el.clientWidth >= el.scrollWidth - 4)
  }
  // Keep the current section's tab in view as the page scrolls.
  useEffect(() => {
    const el = row.current?.querySelector<HTMLElement>('[aria-current="location"]')
    el?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    onRowScroll()
  }, [active])
  return (
    <>
      <nav
        aria-label="Settings sections"
        className="sticky top-[3.2rem] z-20 -mx-[0.85rem] mb-6 border-b border-border bg-base/90 px-[0.85rem] py-2 backdrop-blur md:top-0 md:mx-0 md:rounded-xl md:border md:px-2 lg:hidden"
      >
        {/* The faded right edge says "there's more, scroll"; it clears once you reach the end. */}
        <div ref={row} onScroll={onRowScroll} data-end={atEnd ? '' : undefined} className="settings-tabs-row flex gap-1 overflow-x-auto [scrollbar-width:none]">
          {sections.map((s) => link(s, 'row'))}
        </div>
      </nav>
      <nav aria-label="Settings sections" className="hidden lg:block">
        <div className="sticky top-8 space-y-0.5">{sections.map((s) => link(s, 'col'))}</div>
      </nav>
    </>
  )
}

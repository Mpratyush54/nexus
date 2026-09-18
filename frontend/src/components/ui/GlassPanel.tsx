import { type HTMLAttributes, type ReactNode } from 'react'

type Props = HTMLAttributes<HTMLDivElement> & {
  children: ReactNode
  glow?: 'none' | 'amber' | 'accent'
}

const glows = {
  none: 'border-border',
  amber: 'border-amber/40 shadow-[var(--shadow-glow-amber)]',
  accent: 'border-border-strong',
}

export function GlassPanel({
  children,
  glow = 'none',
  className = '',
  ...props
}: Props) {
  return (
    <div
      className={[
        'rounded-xl border bg-surface',
        'transition-colors duration-200',
        glows[glow],
        className,
      ].join(' ')}
      {...props}
    >
      {children}
    </div>
  )
}

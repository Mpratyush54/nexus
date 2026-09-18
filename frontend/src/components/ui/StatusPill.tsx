type Tone = 'neutral' | 'teal' | 'amber' | 'accent' | 'danger' | 'ember'

const tones: Record<Tone, string> = {
  neutral: 'text-fg-dim',
  teal: 'text-teal',
  amber: 'text-amber',
  accent: 'text-fg',
  danger: 'text-danger',
  ember: 'text-ember',
}

type Props = {
  children: string
  tone?: Tone
  className?: string
}

export function StatusPill({ children, tone = 'neutral', className = '' }: Props) {
  return (
    <span
      className={[
        'inline-flex items-center font-mono text-[11px] tracking-wide',
        tones[tone],
        className,
      ].join(' ')}
    >
      {children}
    </span>
  )
}

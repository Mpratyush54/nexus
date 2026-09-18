import { StatusPill } from '../components/ui/StatusPill'

type Props = {
  title: string
  blurb: string
  tone?: 'neutral' | 'teal' | 'amber' | 'accent' | 'ember'
}

export function PlaceholderPage({ title, blurb, tone = 'neutral' }: Props) {
  return (
    <div className="flex min-h-[50vh] flex-col items-start justify-center">
      <StatusPill tone={tone} className="mb-3">
        soon
      </StatusPill>
      <h1 className="text-2xl font-semibold tracking-tight text-fg">{title}</h1>
      <p className="mt-2 max-w-md text-sm text-fg-dim">{blurb}</p>
    </div>
  )
}

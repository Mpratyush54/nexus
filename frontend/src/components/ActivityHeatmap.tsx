import { useMemo } from 'react'
import type { HeatDay } from '@/api/dashboard'

type Props = {
  days?: HeatDay[]
  loading?: boolean
}

function levelFor(count: number): 0 | 1 | 2 | 3 | 4 {
  if (count <= 0) return 0
  if (count === 1) return 1
  if (count <= 3) return 2
  if (count <= 6) return 3
  return 4
}

const LEVEL_BG = [
  'var(--color-raised)',
  'color-mix(in srgb, var(--color-amber) 28%, var(--color-raised))',
  'color-mix(in srgb, var(--color-amber) 48%, var(--color-raised))',
  'color-mix(in srgb, var(--color-amber) 72%, var(--color-base))',
  'var(--color-amber)',
] as const

/** GitHub-style contribution grid from dashboard heatmap (~52 weeks). */
export function ActivityHeatmap({ days = [], loading }: Props) {
  const { cells, weeks, total, monthLabels } = useMemo(() => {
    const byDate = new Map(days.map((d) => [d.date, d.count]))
    const today = new Date()
    today.setUTCHours(0, 0, 0, 0)

    // Align to Sunday start like GitHub (UTC).
    const end = new Date(today)
    const start = new Date(today)
    start.setUTCDate(start.getUTCDate() - 364)
    while (start.getUTCDay() !== 0) {
      start.setUTCDate(start.getUTCDate() - 1)
    }

    const out: { date: string; count: number; level: 0 | 1 | 2 | 3 | 4 }[] = []
    for (let d = new Date(start); d <= end; d.setUTCDate(d.getUTCDate() + 1)) {
      const key = d.toISOString().slice(0, 10)
      const count = byDate.get(key) ?? 0
      out.push({ date: key, count, level: levelFor(count) })
    }

    const weekCount = Math.ceil(out.length / 7)
    const labels: { label: string; week: number }[] = []
    let lastMonth = -1
    for (let w = 0; w < weekCount; w++) {
      const cell = out[w * 7]
      if (!cell) continue
      const month = new Date(cell.date + 'T00:00:00Z').getUTCMonth()
      if (month !== lastMonth) {
        labels.push({
          label: new Date(cell.date + 'T00:00:00Z').toLocaleString('en', {
            month: 'short',
            timeZone: 'UTC',
          }),
          week: w,
        })
        lastMonth = month
      }
    }

    return {
      cells: out,
      weeks: weekCount,
      total: out.reduce((s, c) => s + c.count, 0),
      monthLabels: labels,
    }
  }, [days])

  if (loading && !days.length) {
    return <p className="text-sm text-muted">Loading activity…</p>
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <p className="text-sm text-fg-dim">
          <span className="font-medium text-fg">{total}</span> project events in the last year
        </p>
        <div className="flex items-center gap-1 text-[10px] text-muted">
          Less
          {[0, 1, 2, 3, 4].map((lvl) => (
            <span
              key={lvl}
              className="inline-block h-2.5 w-2.5 rounded-[2px]"
              style={{ background: LEVEL_BG[lvl as 0 | 1 | 2 | 3 | 4] }}
            />
          ))}
          More
        </div>
      </div>

      <div className="overflow-x-auto pb-1">
        <div className="inline-block min-w-max">
          <div
            className="mb-1 grid gap-[3px] text-[10px] text-muted"
            style={{ gridTemplateColumns: `repeat(${weeks}, 11px)`, marginLeft: 28 }}
          >
            {monthLabels.map((m) => (
              <span
                key={`${m.label}-${m.week}`}
                style={{ gridColumn: m.week + 1 }}
                className="truncate"
              >
                {m.label}
              </span>
            ))}
          </div>
          <div className="flex gap-2">
            <div className="flex w-6 flex-col justify-between py-[1px] text-[9px] leading-none text-muted">
              <span />
              <span>Mon</span>
              <span />
              <span>Wed</span>
              <span />
              <span>Fri</span>
              <span />
            </div>
            <div
              className="grid grid-flow-col gap-[3px]"
              style={{
                gridTemplateRows: 'repeat(7, 11px)',
                gridAutoColumns: '11px',
              }}
            >
              {cells.map((c) => (
                <span
                  key={c.date}
                  title={`${c.date}: ${c.count} event${c.count === 1 ? '' : 's'}`}
                  className="rounded-[2px]"
                  style={{ background: LEVEL_BG[c.level], width: 11, height: 11 }}
                />
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

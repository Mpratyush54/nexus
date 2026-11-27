import { useEffect, useRef, type ReactNode } from 'react'
import { segments } from '@/lib/composer-commands'

export type PopupRow = {
  id: string
  icon: ReactNode
  title: string
  titleHits: number[]
  detail?: string
  group: string
  badge?: string
}

/**
 * The list above the composer for "/" and "@". It never takes focus: the
 * textarea keeps the caret and drives the selection (↑/↓, Enter/Tab, Esc),
 * so typing is never blocked. Matched letters are highlighted; an empty
 * list says so. Mirrors desktop/windows/Pages/ComposerPopup.cs.
 */
export function ComposerPopup({
  id,
  title,
  rows,
  selected,
  empty,
  loading,
  onHover,
  onPick,
}: {
  id: string
  title: string
  rows: PopupRow[]
  selected: number
  empty: string
  loading?: boolean
  onHover: (index: number) => void
  onPick: (index: number) => void
}) {
  const list = useRef<HTMLUListElement>(null)
  useEffect(() => {
    const el = list.current?.querySelector<HTMLElement>(`[data-index="${selected}"]`)
    el?.scrollIntoView?.({ block: 'nearest' })
  }, [selected])
  // A small heading above the first row of each group.
  const headings = rows.map((row, i) => (row.group && (i === 0 || rows[i - 1].group !== row.group) ? row.group : ''))
  return (
    <div className="composer-popup" role="presentation">
      <p className="composer-popup-title">{title}</p>
      {rows.length > 0 ? (
        <ul id={id} ref={list} role="listbox" aria-label={title}>
          {rows.map((row, index) => {
            const heading = headings[index]
            return (
              <li key={row.id} role="none">
                {heading && (
                  <span className="composer-popup-group" aria-hidden>
                    {heading}
                  </span>
                )}
                <div
                  id={`${id}-${index}`}
                  role="option"
                  aria-selected={index === selected}
                  data-index={index}
                  className={`composer-popup-row ${index === selected ? 'is-selected' : ''}`}
                  // Mouse down, not click: the textarea must keep focus.
                  onMouseDown={(e) => {
                    e.preventDefault()
                    onPick(index)
                  }}
                  onMouseEnter={() => onHover(index)}
                >
                  <span className="composer-popup-icon" aria-hidden>
                    {row.icon}
                  </span>
                  <span className="composer-popup-text">
                    <span className="composer-popup-name">
                      {segments(row.title, row.titleHits).map((part, k) =>
                        part.hit ? <mark key={k}>{part.text}</mark> : <span key={k}>{part.text}</span>,
                      )}
                    </span>
                    {row.detail && <span className="composer-popup-detail">{row.detail}</span>}
                  </span>
                  {row.badge && <span className="composer-popup-badge">{row.badge}</span>}
                </div>
              </li>
            )
          })}
        </ul>
      ) : (
        <p className="composer-popup-empty" role="status">
          {loading ? 'Looking…' : empty}
        </p>
      )}
      <p className="composer-popup-hint" aria-hidden>
        ↑↓ move · Enter or Tab pick · Esc close
      </p>
    </div>
  )
}

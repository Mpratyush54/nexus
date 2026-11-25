import { useEffect, useState } from 'react'

/** Shared input look for settings forms (matches the rest of the portal). */
export const inputClass =
  'h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none transition-colors placeholder:text-muted focus:border-amber disabled:opacity-60'

/** Highlights the section currently in view. */
export function useActiveSection(ids: string[]) {
  const [active, setActive] = useState(ids[0])
  const key = ids.join(',')
  useEffect(() => {
    if (typeof IntersectionObserver === 'undefined') return
    const els = key
      .split(',')
      .map((id) => document.getElementById(id))
      .filter((el): el is HTMLElement => Boolean(el))
    const obs = new IntersectionObserver(
      (entries) => {
        const hit = entries.filter((e) => e.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0]
        if (hit) setActive(hit.target.id)
      },
      { rootMargin: '-20% 0px -65% 0px' },
    )
    els.forEach((el) => obs.observe(el))
    return () => obs.disconnect()
  }, [key])
  return active
}

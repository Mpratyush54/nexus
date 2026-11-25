import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { ArrowRight, ChevronDown, ChevronUp, Download } from 'lucide-react'
import { NexusMark } from '@/components/NexusBrand'
import { DOWNLOAD_URL, SIGN_UP_PATH } from '@/landing/content'

/** The app shell's in-flow slot at the top of the page column. */
function useTopSlot() {
  const [slot, setSlot] = useState<HTMLElement | null>(null)
  useEffect(() => {
    // The shell mounts lazily and remounts the slot when it switches layout,
    // so keep following it.
    const find = () => {
      const next = document.getElementById('app-top-slot')
      setSlot((prev) => (prev === next ? prev : next))
    }
    find()
    const observer = new MutationObserver(find)
    observer.observe(document.body, { childList: true, subtree: true })
    return () => observer.disconnect()
  }, [])
  return slot
}

/**
 * Always-visible strip that says this is sample data and offers the real
 * thing. It sits in the page flow at the top of the content column, so it
 * never covers content or the phone tab bar. Its links are plain anchors on
 * purpose: they leave the demo with a full page load, so the real app starts
 * with real storage and network.
 */
export function DemoBanner() {
  const [open, setOpen] = useState(true)
  const slot = useTopSlot()

  useEffect(() => {
    document.documentElement.classList.toggle('nx-demo-min', !open)
  }, [open])

  const bar = (
    <aside className={['demo-banner', open ? '' : 'is-min', slot ? 'is-docked' : ''].filter(Boolean).join(' ')} aria-label="Demo">
      <span className="demo-banner-tag">
        <NexusMark size={16} />
        <strong>Demo</strong>
        <span className="demo-banner-dim">— sample data</span>
      </span>
      {open ? <span className="demo-banner-text">A fictional team. Changes stay in this tab.</span> : null}
      <span className="demo-banner-actions">
        <a className="demo-banner-cta" href={SIGN_UP_PATH}>
          Sign up free <ArrowRight size={14} aria-hidden="true" />
        </a>
        {open ? (
          <>
            <a className="demo-banner-link demo-banner-download" href={DOWNLOAD_URL}>
              <Download size={14} aria-hidden="true" /> Download
            </a>
            <a className="demo-banner-link" href="/">Exit demo</a>
          </>
        ) : null}
        <button
          type="button"
          className="demo-banner-toggle"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          aria-label={open ? 'Make the demo bar smaller' : 'Show the full demo bar'}
        >
          {open ? <ChevronUp size={16} aria-hidden="true" /> : <ChevronDown size={16} aria-hidden="true" />}
        </button>
      </span>
    </aside>
  )

  return slot ? createPortal(bar, slot) : bar
}

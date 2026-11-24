import { useState } from 'react'
import { ArrowRight, ChevronDown, ChevronUp, Download } from 'lucide-react'
import { NexusMark } from '@/components/NexusBrand'
import { DOWNLOAD_URL, SIGN_UP_PATH } from '@/landing/content'

/**
 * Always-visible strip that says this is sample data and offers the real
 * thing. Its links are plain anchors on purpose: they leave the demo with a
 * full page load, so the real app starts with real storage and network.
 */
export function DemoBanner() {
  const [open, setOpen] = useState(true)
  return (
    <aside className={`demo-banner ${open ? '' : 'is-min'}`} aria-label="Demo">
      <span className="demo-banner-tag">
        <NexusMark size={18} />
        <strong>Demo</strong>
        <span className="demo-banner-dim">— sample data</span>
      </span>
      {open ? (
        <>
          <span className="demo-banner-text">A fictional team. Changes stay in this tab.</span>
          <a className="demo-banner-cta" href={SIGN_UP_PATH}>
            Sign up free <ArrowRight size={14} aria-hidden="true" />
          </a>
          <a className="demo-banner-link" href={DOWNLOAD_URL}>
            <Download size={14} aria-hidden="true" /> Download
          </a>
          <a className="demo-banner-link" href="/">Exit demo</a>
        </>
      ) : null}
      <button type="button" className="demo-banner-toggle" onClick={() => setOpen((v) => !v)} aria-expanded={open} aria-label={open ? 'Minimise demo bar' : 'Expand demo bar'}>
        {open ? <ChevronDown size={16} aria-hidden="true" /> : <ChevronUp size={16} aria-hidden="true" />}
      </button>
    </aside>
  )
}

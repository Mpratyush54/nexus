import { Suspense, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useLocation, useOutlet } from 'react-router-dom'
import { AnimatePresence, motion, useReducedMotion } from 'framer-motion'
import { NexusMark } from '@/components/NexusBrand'
import { MemoryGraph } from './MemoryGraph'
import './auth.css'

const EASE = [0.22, 1, 0.36, 1] as const

/**
 * Shared frame for sign-in, sign-up and password reset: the form on the left,
 * a dark product panel on the right (desktop only). As a layout route it stays
 * mounted while the user moves between those pages, so only the form content
 * cross-fades and the column eases to its new height.
 */
export function AuthLayout({ children, topAction }: { children?: ReactNode; topAction?: ReactNode }) {
  const outlet = useOutlet()
  const location = useLocation()
  const reduce = useReducedMotion()
  const content = children ?? outlet

  // Warm the sibling pages so switching between them never shows a blank stage.
  useEffect(() => {
    const w = window as Window & { requestIdleCallback?: (cb: () => void) => number }
    const idle = (cb: () => void) => (w.requestIdleCallback ? w.requestIdleCallback(cb) : window.setTimeout(cb, 400))
    idle(() => {
      void import('@/pages/LoginPage')
      void import('@/pages/SignupPage')
      void import('@/pages/ForgotPasswordPage')
    })
  }, [])

  const path = location.pathname
  const top =
    topAction ??
    (path.endsWith('/signup') ? (
      <Link className="au-top-link" to={{ pathname: '/login', search: location.search }}>
        Have an account? <strong>Log in</strong>
      </Link>
    ) : (
      <Link className="au-top-link" to={{ pathname: '/signup', search: location.search }}>
        New to Nexus? <strong>Sign up</strong>
      </Link>
    ))

  return (
    <div className="au">
      <div className="au-split">
        <div className="au-side">
          <header className="au-top">
            <a className="au-home" href="/" aria-label="Nexus home">
              <NexusMark size={30} />
              <span>Nexus</span>
            </a>
            {top}
          </header>
          <main className="au-main" id="main">
            <div className="au-stage">
              <AutoHeight reduce={Boolean(reduce)}>
                <AnimatePresence mode="wait" initial={false}>
                  <motion.div
                    key={children ? 'static' : path}
                    initial={reduce ? false : { opacity: 0, y: 8 }}
                    animate={{ opacity: 1, y: 0 }}
                    exit={reduce ? { opacity: 0, transition: { duration: 0 } } : { opacity: 0, y: -6 }}
                    transition={{ duration: 0.2, ease: EASE }}
                  >
                    <Suspense fallback={<div style={{ minHeight: 420 }} />}>{content}</Suspense>
                  </motion.div>
                </AnimatePresence>
              </AutoHeight>
            </div>
          </main>
          <footer className="au-foot">
            <span>© {new Date().getFullYear()} Nexus</span>
            <span style={{ display: 'flex', gap: 18 }}>
              <a href="/#faq">Help</a>
              <a href="https://github.com/Mpratyush54/nexus" target="_blank" rel="noreferrer">
                GitHub
              </a>
            </span>
          </footer>
        </div>
        <ProductPanel />
      </div>
    </div>
  )
}

/** Animates the stage height so a taller or shorter form never jumps. */
function AutoHeight({ children, reduce }: { children: ReactNode; reduce: boolean }) {
  const inner = useRef<HTMLDivElement>(null)
  const [height, setHeight] = useState<number | 'auto'>('auto')

  useLayoutEffect(() => {
    const el = inner.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(([entry]) => {
      const h = entry.borderBoxSize?.[0]?.blockSize ?? el.offsetHeight
      // A zero height is a page mid-swap; keep the old height until the next one lands.
      // +12: the wrapper is border-box with 6px padding (room for focus rings).
      if (h > 0) setHeight(h + 12)
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  return (
    <motion.div
      animate={{ height }}
      initial={false}
      transition={reduce ? { duration: 0 } : { duration: 0.28, ease: EASE }}
      style={{ overflow: 'hidden', margin: '-6px', padding: '6px' }}
    >
      <div ref={inner}>{children}</div>
    </motion.div>
  )
}

function ProductPanel() {
  return (
    <aside className="au-visual" aria-label="About Nexus">
      <div className="au-visual-inner">
        <div className="au-visual-copy">
          <p className="au-eyebrow">shared memory · 6 agents</p>
          <h2 className="au-visual-title">
            One memory for every coding agent <mark>you use.</mark>
          </h2>
          <p className="au-visual-lede">
            Claude Code, Codex, Cursor, OpenCode, Gemini and Kimi pick up what your team already
            decided, and why, instead of starting from zero.
          </p>
        </div>
        <MemoryGraph />
        <figure className="au-shot">
          <div className="au-shot-bar">
            <i />
            <i />
            <i />
            <span>Nexus for Windows · Memory</span>
          </div>
          <picture>
            <source type="image/avif" srcSet="/landing/shots/memory-800.avif 800w, /landing/shots/memory-1400.avif 1400w" sizes="50vw" />
            <source type="image/webp" srcSet="/landing/shots/memory-800.webp 800w, /landing/shots/memory-1400.webp 1400w" sizes="50vw" />
            <img
              src="/landing/shots/memory-1400.png"
              width={1400}
              height={840}
              alt="The Nexus Windows app showing a project's shared memory: decisions captured from agent sessions."
              loading="lazy"
              decoding="async"
            />
          </picture>
        </figure>
      </div>
    </aside>
  )
}

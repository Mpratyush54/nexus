import { Component, useEffect, useRef, useState, type ErrorInfo, type ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { NexusMark } from '@/components/NexusBrand'
import { handoff, useHandoff } from './handoff'
import { reportError } from '@/lib/diagnostics'
import './auth.css'

/**
 * The app's loading screen. index.html draws the same thing (#nx-boot) before
 * any script runs, so a cold load goes splash → page without a blank frame.
 */
export function BootSplash({ label = 'Loading Nexus' }: { label?: string }) {
  return (
    <div className="au">
      <div className="au-boot" role="status" aria-live="polite">
        <div className="au-boot-mark">
          <NexusMark size={44} />
          <div className="au-boot-bar" aria-hidden>
            <i />
          </div>
          <span className="au-sr">{label}…</span>
        </div>
      </div>
    </div>
  )
}

/**
 * After sign-in: covers the form, lets the app route load underneath and
 * fades away once the app has drawn, so there is no white or black flash
 * between the two.
 */
export function HandoffCover() {
  const phase = useHandoff()
  // Mounted fresh for each hand-off, so its state starts clean every time.
  return phase === 'covering' ? <Cover /> : null
}

function Cover() {
  const location = useLocation()
  const [fading, setFading] = useState(false)
  const startPath = useRef(location.pathname)

  useEffect(() => {
    if (location.pathname === startPath.current) return
    // The destination route has rendered at least its loading state; give its
    // data a moment, then reveal.
    let done = false
    const reveal = () => {
      if (done) return
      done = true
      setFading(true)
      window.setTimeout(() => handoff.release(), 320)
    }
    const started = performance.now()
    const tick = () => {
      if (done) return
      const appDrawn = document.querySelector('.app-main, .au-ob, .au-done, .au-device')
      if (appDrawn || performance.now() - started > 2500) {
        window.setTimeout(reveal, 180)
        return
      }
      requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
    return () => {
      done = true
    }
  }, [location.pathname])

  // Never leave the cover up if the destination does not draw what we look for.
  useEffect(() => {
    const t = window.setTimeout(() => handoff.release(), 6000)
    return () => window.clearTimeout(t)
  }, [])

  return (
    <div
      className="au"
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 200,
        minHeight: 0,
        background: 'transparent',
        pointerEvents: 'none',
      }}
    >
      <div className={`au-boot au-handoff ${fading ? 'is-out' : ''}`} role="status" aria-live="polite">
        <div className="au-boot-mark">
          <NexusMark size={44} />
          <div className="au-boot-bar" aria-hidden>
            <i />
          </div>
          <span>Opening your workspace…</span>
        </div>
      </div>
    </div>
  )
}

function isChunkError(error: Error | null) {
  const m = error?.message ?? ''
  return /dynamically imported module|Importing a module script failed|Failed to fetch|error loading dynamically|ChunkLoadError/i.test(m)
}

type BoundaryState = { error: Error | null; key?: string; reported?: boolean }

/**
 * Catches render errors anywhere below it. A failed page download (usually a
 * new release replacing old files) gets a reload; anything else gets a retry
 * that re-renders without a page load.
 */
export class AppErrorBoundary extends Component<{ children: ReactNode; resetKey?: string }, BoundaryState> {
  state: BoundaryState = { error: null, key: this.props.resetKey }

  static getDerivedStateFromError(error: Error): BoundaryState {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Nexus: screen failed to render', error, info.componentStack)
    // Chunk errors are a stale tab after a deploy, not a bug.
    if (!isChunkError(error) && reportError(error, 'crash', { componentStack: info.componentStack ?? undefined, via: 'AppErrorBoundary' })) {
      this.setState({ reported: true })
    }
  }

  // Navigating away clears the error so the next page gets a fresh try.
  static getDerivedStateFromProps(props: { resetKey?: string }, state: BoundaryState): Partial<BoundaryState> | null {
    if (props.resetKey === state.key) return null
    return { key: props.resetKey, error: null, reported: false }
  }

  render() {
    const { error } = this.state
    if (!error) return this.props.children
    const chunk = isChunkError(error)
    return (
      <EdgeFrame>
        <p className="au-kicker">{chunk ? 'update available' : 'something broke'}</p>
        <h1 className="au-title" style={{ marginTop: 12 }}>
          {chunk ? 'Nexus was updated while this tab was open' : 'This screen hit a problem'}
        </h1>
        <p className="au-sub">
          {chunk
            ? 'Part of the page could not load, usually because a newer version is out. Reload to get it; nothing you saved is lost.'
            : 'Your data is safe: nothing was changed. Try again, and if it keeps happening, reload the page.'}
        </p>
        <div className="au-edge-actions">
          {chunk ? (
            <button type="button" className="au-btn" onClick={() => window.location.reload()}>
              Reload Nexus
            </button>
          ) : (
            <>
              <button type="button" className="au-btn" onClick={() => this.setState({ error: null })}>
                Try again
              </button>
              <button type="button" className="au-btn is-secondary" onClick={() => window.location.reload()}>
                Reload page
              </button>
            </>
          )}
        </div>
        <p className="au-edge-detail">{error.message.slice(0, 240)}</p>
        {this.state.reported ? (
          <p className="au-sub" style={{ fontSize: 13, marginTop: 12 }}>
            An error report was sent to the Nexus team: where in the code it failed, the app version and your browser, with no chats or other content. You can turn this off in Settings.
          </p>
        ) : null}
      </EdgeFrame>
    )
  }
}

/** Branded frame for full-page states: 404, errors. */
export function EdgeFrame({ children }: { children: ReactNode }) {
  return (
    <div className="au">
      <div className="au-edge">
        <header className="au-top">
          <a className="au-home" href="/" aria-label="Nexus home">
            <NexusMark size={30} />
            <span>Nexus</span>
          </a>
        </header>
        <main className="au-edge-main" id="main">
          <div className="au-edge-card">{children}</div>
        </main>
      </div>
    </div>
  )
}

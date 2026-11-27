import {
  ArrowRight,
  Check,
  ChevronDown,
  Download,
  Feather,
  Gauge,
  History,
  Maximize,
  Menu,
  Minimize,
  MousePointerClick,
  Pause,
  Play,
  Plug,
  ShieldCheck,
  Smartphone,
  Sparkles,
  Users,
  Volume2,
  VolumeX,
  X,
} from 'lucide-react'
import type { CSSProperties, ReactNode } from 'react'
import {
  AGENTS,
  DEMO_PATH,
  DOWNLOAD_URL,
  MAC_DOWNLOAD_URL,
  FILM_DURATION,
  FAQ,
  LATEST_VERSION,
  SIGN_IN_PATH,
  SIGN_UP_PATH,
  WEB_APP_PATH,
} from './content'
import { SiteFooter } from './SiteFooter'

/*
 * The public landing page. Rendered to static HTML at build time
 * (scripts/prerender.tsx); the browser only runs src/landing/client.ts,
 * never React, so everything here must work as plain markup.
 *
 * One story: every coding agent in one app, with one shared memory.
 * Hero with the "how it works" pipeline → tabbed showcase (Chat, Control,
 * Memory, Live) → film and the rest → FAQ and download. Dark only (DESIGN.md).
 */

/* ---------- OS-aware downloads ---------- */

// Each variant carries data-for; CSS shows the ones matching <html data-os>
// (set by client.ts from src/landing/os.ts). Without JS the Windows set shows.
function DownloadCta({ size = 'lg' }: { size?: 'lg' | 'sm' }) {
  const sm = size === 'sm'
  const cls = `nx-btn nx-btn-primary nx-cta-dl${sm ? ' nx-btn-sm' : ''}`
  const icon = <Download size={sm ? 15 : 17} aria-hidden="true" />
  return (
    <>
      <a className={cls} data-for="win" href={DOWNLOAD_URL} aria-label={sm ? 'Download Nexus for Windows' : undefined}>
        {icon} {sm ? 'Download' : 'Download for Windows'}
      </a>
      <a className={cls} data-for="mac" href={MAC_DOWNLOAD_URL} aria-label={sm ? 'Download the Nexus macOS beta' : undefined}>
        {icon} {sm ? 'Download' : 'Download for Mac'}
      </a>
      <a className={`nx-btn nx-btn-primary nx-cta-dl${sm ? ' nx-btn-sm' : ''}`} data-for="mobile other" href={DEMO_PATH}>
        <MousePointerClick size={sm ? 15 : 16} aria-hidden="true" /> {sm ? 'Live demo' : 'Try the live demo'}
      </a>
    </>
  )
}

function CtaGroup() {
  return (
    <>
      <div className="nx-cta">
        <DownloadCta />
        <a className="nx-btn nx-btn-secondary" data-for="win mac" href={DEMO_PATH}>
          <MousePointerClick size={16} aria-hidden="true" /> Try the live demo
        </a>
      </div>
      <p className="nx-cta-note">
        <span data-for="win">
          Free plan <span aria-hidden="true">·</span> Also for <a href="#mac">Mac (beta)</a> and the <a href={WEB_APP_PATH}>web</a>
        </span>
        <span data-for="mac">
          Free plan <span aria-hidden="true">·</span> Apple silicon beta, <a href="#mac">install notes</a> <span aria-hidden="true">·</span> Also
          for <a href={DOWNLOAD_URL}>Windows</a>
        </span>
        <span data-for="mobile">
          Nexus runs on your computer. Get it for <a href={DOWNLOAD_URL}>Windows</a> <span aria-hidden="true">·</span>{' '}
          <a href={MAC_DOWNLOAD_URL}>Mac</a>
        </span>
        <span data-for="other">
          Desktop app for <a href={DOWNLOAD_URL}>Windows</a> <span aria-hidden="true">·</span> <a href={MAC_DOWNLOAD_URL}>Mac (beta)</a>. The{' '}
          <a href={WEB_APP_PATH}>web app</a> works everywhere.
        </span>
      </p>
    </>
  )
}

/* ---------- how it works: agents → Nexus → what you get ---------- */

const OUTCOMES = [
  { icon: Sparkles, t: 'Your next chat starts with context', d: 'Any agent picks up what the last one decided.' },
  { icon: History, t: 'Ask why a line changed', d: 'See the agent, the chat and the reason.' },
  { icon: Smartphone, t: 'Follow along anywhere', d: 'The same chats live on your phone and the web.' },
  { icon: Users, t: 'Hand off to a teammate', d: 'Share a chat with its full history.' },
]

const STAGES = [
  { t: 'Captures every chat', d: 'From the app, a terminal or your editor' },
  { t: 'Removes secrets', d: 'Keys and passwords never get saved' },
  { t: 'Keeps one memory', d: 'Decisions, a project brief, line history' },
]

// Curves from each item to the middle of the column next to it. The SVG
// stretches to the row height; item centres sit at (2i + 1) / 2n of it.
function Wires({ n, side }: { n: number; side: 'in' | 'out' }) {
  return (
    <svg className={`nx-wires nx-wires-${side}`} viewBox="0 0 100 120" preserveAspectRatio="none" aria-hidden="true" focusable="false">
      {Array.from({ length: n }, (_, i) => {
        const y = ((2 * i + 1) / (2 * n)) * 120
        const d = side === 'in' ? `M0 ${y} C 55 ${y}, 45 60, 100 60` : `M0 60 C 55 60, 45 ${y}, 100 ${y}`
        return (
          <g key={i} style={{ '--i': i } as CSSProperties}>
            <path className="nx-wire" d={d} />
            <path className="nx-wire-flow" d={d} />
          </g>
        )
      })}
    </svg>
  )
}

function Pipeline() {
  return (
    <figure className="nx-flow" aria-labelledby="flow-title">
      <figcaption id="flow-title" className="nx-sr">
        How Nexus works: chats from Claude Code, Codex, Cursor, OpenCode, Gemini and Kimi flow into Nexus, which captures them, removes
        secrets and keeps one memory. Out of it: your next chat starts with context, any agent can ask why a line changed, you can follow
        along on your phone or the web, and you can hand off to a teammate.
      </figcaption>
      <div className="nx-flow-col nx-flow-agents">
        <p className="nx-flow-h">Your agents</p>
        <ul>
          {AGENTS.map((a) => (
            <li key={a.id} className="nx-flow-agent" data-agent={a.id}>
              <span className="nx-agent-dot" aria-hidden="true" />
              {a.name}
            </li>
          ))}
        </ul>
      </div>
      <Wires n={AGENTS.length} side="in" />
      <div className="nx-flow-core">
        <div className="nx-flow-core-head">
          <img src="/brand/nexus-app.svg" width="36" height="36" alt="" />
          <span>Nexus</span>
        </div>
        <ol>
          {STAGES.map((s, i) => (
            <li key={s.t} style={{ '--i': i } as CSSProperties}>
              <span className="nx-flow-step" aria-hidden="true">{i + 1}</span>
              <span>
                <strong>{s.t}</strong>
                <small>{s.d}</small>
              </span>
            </li>
          ))}
        </ol>
      </div>
      <Wires n={OUTCOMES.length} side="out" />
      <div className="nx-flow-col nx-flow-out">
        <p className="nx-flow-h">What you get</p>
        <ul>
          {OUTCOMES.map(({ icon: Icon, t, d }) => (
            <li key={t}>
              <Icon size={18} strokeWidth={1.75} aria-hidden="true" />
              <span>
                <strong>{t}</strong>
                <small>{d}</small>
              </span>
            </li>
          ))}
        </ul>
      </div>
    </figure>
  )
}

/* ---------- product screenshots ---------- */

// Real captures of the Nexus web app on the demo's sample data (a fictional
// team; never real chats), taken in headless Chrome at 2x (3x for the phone).
// Each ships as AVIF and WebP at its full size and at half, with a PNG
// fallback; the page never shows one wider than half its pixel width.
type ShotSpec = { name: string; w: number; h: number; label: string; alt: string }

const SHOTS = {
  chat: { name: 'web-chat', w: 1600, h: 1140, label: 'Chat', alt: 'A chat with Claude Code in Nexus: the question about reusing the order key, then the agent thinking live while it reads, edits and tests src/payments/retry.ts, with Esc to stop.' },
  steps: { name: 'web-steps', w: 1600, h: 1140, label: 'Steps', alt: 'The Steps tab of a chat: each edit the agent made to src/payments/retry.ts shown as a diff, followed by the test file it wrote and the test command it ran.' },
  memory: { name: 'web-memory', w: 1872, h: 1337, label: 'Memory', alt: 'The Memory page for the checkout-service project: twelve memories grouped by topic, starting with the project brief and the capture retry rules, each saved by an agent and editable.' },
  phone: { name: 'web-phone', w: 1170, h: 2400, label: 'Phone', alt: 'The same chat on a phone: Claude Code thinking live and running its steps, with a reply box and a stop button.' },
} satisfies Record<string, ShotSpec>

function Shot({ s, sizes, className = '' }: { s: ShotSpec; sizes: string; className?: string }) {
  const base = `/landing/shots/${s.name}`
  const half = Math.round(s.w / 2)
  const set = (ext: string) => `${base}-${half}.${ext} ${half}w, ${base}-${s.w}.${ext} ${s.w}w`
  return (
    <figure className={`nx-shot ${className}`} style={{ '--ratio': `${s.w} / ${s.h}`, '--max': `${half}px` } as CSSProperties}>
      {/* Opens the full-size capture, which phones can pinch-zoom. */}
      <a className="nx-shot-win" href={`${base}-${s.w}.png`} target="_blank" rel="noopener">
        <picture>
          <source type="image/avif" srcSet={set('avif')} sizes={sizes} />
          <source type="image/webp" srcSet={set('webp')} sizes={sizes} />
          <img src={`${base}-${s.w}.png`} width={s.w} height={s.h} alt={s.alt} loading="lazy" decoding="async" />
        </picture>
      </a>
      <figcaption className="nx-shot-cap">
        <img src="/brand/nexus-app.svg" width="14" height="14" alt="" />
        <span>
          Nexus on the web <span aria-hidden="true">·</span> {s.label}
        </span>
      </figcaption>
    </figure>
  )
}

/* ---------- tabbed showcase (tabs wired up in client.ts) ---------- */

const Kbd = ({ children }: { children: ReactNode }) => <kbd className="nx-kbd">{children}</kbd>
const Code = ({ children }: { children: ReactNode }) => <code className="nx-inline-code">{children}</code>

type Tab = { id: string; tab: string; title: string; points: ReactNode[]; shot: ShotSpec }

const TABS: Tab[] = [
  {
    id: 'chat',
    tab: 'Chat',
    title: 'Pick any agent. Leave it running.',
    shot: SHOTS.chat,
    points: [
      'Chats keep going in the background, several at once. You get a notification when one is done or needs you.',
      'Approve commands right in the chat: once, for this chat, or always in this project.',
      <>
        <Kbd>@</Kbd> a file, chat or memory, <Kbd>/</Kbd> for commands, voice input, and <Kbd>Alt</Kbd>+<Kbd>Space</Kbd> to ask from
        anywhere.
      </>,
    ],
  },
  {
    id: 'control',
    tab: 'Control',
    title: 'Keep what’s good. Undo the rest.',
    shot: SHOTS.steps,
    points: [
      'Every edit shows up as a diff. Keep or undo a whole file or a single change.',
      <>
        Go back to any message, files included, or just type <Code>/rewind</Code>.
      </>,
      'Ask every time, approve edits automatically, or give full access. Trusted commands stop asking.',
    ],
  },
  {
    id: 'memory',
    tab: 'Memory',
    title: 'One memory for every agent.',
    shot: SHOTS.memory,
    points: [
      'What each chat decided is saved as a memory any agent can search mid-task.',
      'A short project brief that every agent reads first.',
      'Memories are flagged when the code they describe changes, and every line keeps the chat behind it.',
    ],
  },
  {
    id: 'live',
    tab: 'Live',
    title: 'Watch it think, from anywhere.',
    shot: SHOTS.phone,
    points: [
      'Replies stream in with the agent’s thinking and every step as it happens.',
      'Open the web app on your phone and follow, or answer, the same chat.',
      'Running chats are marked in the list, so you know what’s still going.',
    ],
  },
]

const SHOT_SIZES = '(min-width: 1288px) 760px, (min-width: 900px) 60vw, calc(100vw - 32px)'

function Showcase() {
  return (
    <section id="features" className="nx-section nx-showcase" aria-labelledby="features-title">
      <div className="nx-wrap">
        <header className="nx-section-head nx-center">
          <h2 id="features-title">Everything in one window.</h2>
          <p className="nx-lede">Chat, check and undo, then pick up later, with every agent, on any screen.</p>
        </header>
        <div className="nx-tabs" role="tablist" aria-label="Features">
          {TABS.map((t, i) => (
            <button
              key={t.id}
              type="button"
              role="tab"
              id={`tab-${t.id}`}
              className="nx-tab"
              aria-selected={i === 0}
              aria-controls={`panel-${t.id}`}
              tabIndex={i === 0 ? 0 : -1}
            >
              {t.tab}
            </button>
          ))}
        </div>
        <div className="nx-panels">
          {TABS.map((t, i) => (
            <div
              key={t.id}
              id={`panel-${t.id}`}
              role="tabpanel"
              aria-labelledby={`tab-${t.id}`}
              className={`nx-panel${i === 0 ? ' is-active' : ''}${t.id === 'live' ? ' nx-panel-phone' : ''}`}
            >
              <div className="nx-panel-copy">
                <h3>{t.title}</h3>
                <ul>
                  {t.points.map((p, j) => (
                    <li key={j}>
                      <Check size={16} strokeWidth={2.25} aria-hidden="true" />
                      <span>{p}</span>
                    </li>
                  ))}
                </ul>
              </div>
              <div className="nx-panel-visual">
                <Shot s={t.shot} sizes={t.id === 'live' ? '300px' : SHOT_SIZES} className={t.id === 'live' ? 'nx-shot-phone' : ''} />
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}

/* ---------- film player (a native modal <dialog>; wired up in client.ts) ---------- */

function FilmPlayer() {
  return (
    <dialog className="nx-player" data-player aria-label="Nexus product film">
      <div className="nx-player-stage" data-player-stage data-state="paused">
        <video className="nx-player-video" data-player-video data-src="/landing/nexus-film-full.mp4" playsInline preload="none" poster="/landing/nexus-film-poster.jpg">
          {/* Music only, no narration: the track describes the sound. */}
          <track kind="captions" srcLang="en" label="English" src="/landing/nexus-film.en.vtt" default />
        </video>
        <button type="button" className="nx-player-big" data-p="toggle" tabIndex={-1} aria-hidden="true">
          <Play size={34} fill="currentColor" strokeWidth={0} />
        </button>
        <div className="nx-player-bar">
          <button type="button" className="nx-player-btn" data-p="play" aria-label="Play">
            <Play className="i-play" size={18} fill="currentColor" strokeWidth={0} aria-hidden="true" />
            <Pause className="i-pause" size={18} fill="currentColor" strokeWidth={0} aria-hidden="true" />
          </button>
          <span className="nx-player-time" data-p="time" aria-hidden="true">0:00</span>
          <input className="nx-player-seek" data-p="seek" type="range" min="0" max="1000" step="1" defaultValue="0" aria-label="Seek" aria-valuetext={`0:00 of ${FILM_DURATION}`} />
          <span className="nx-player-time" data-p="dur" aria-hidden="true">{FILM_DURATION}</span>
          <button type="button" className="nx-player-btn" data-p="mute" aria-label="Mute">
            <Volume2 className="i-vol" size={18} aria-hidden="true" />
            <VolumeX className="i-muted" size={18} aria-hidden="true" />
          </button>
          <button type="button" className="nx-player-btn" data-p="fs" aria-label="Full screen">
            <Maximize className="i-fs" size={17} aria-hidden="true" />
            <Minimize className="i-unfs" size={17} aria-hidden="true" />
          </button>
        </div>
        <button type="button" className="nx-player-close" data-p="close" aria-label="Close film">
          <X size={20} aria-hidden="true" />
        </button>
      </div>
      <p className="nx-player-keys" aria-hidden="true">Space play/pause · ← → 5 s · M mute · F full screen · Esc close</p>
    </dialog>
  )
}

const MORE = [
  { icon: Users, t: 'Built for teams', d: 'Share chats, group projects, take access back.' },
  { icon: ShieldCheck, t: 'Private by default', d: 'Secrets redacted; your memory stays yours.' },
  { icon: Plug, t: 'Agents set up in one place', d: 'Rules files and tool servers, no JSON.' },
  { icon: Gauge, t: 'See what each agent costs', d: 'Tokens and cost per chat, agent and model.' },
  { icon: Feather, t: 'Native and light', d: 'WinUI 3, not Electron. Starts fast.' },
  { icon: Smartphone, t: 'Works on your phone', d: 'The web app has the same chats.' },
]

const NAV = [
  { href: '#how', label: 'How it works' },
  { href: '#features', label: 'Features' },
  { href: '#faq', label: 'FAQ' },
]

/* ---------- page ---------- */

export function LandingPage() {
  return (
    <div className="nx">
      <a className="nx-skip" href="#main">Skip to content</a>
      <header className="nx-nav">
        <div className="nx-wrap nx-nav-in">
          <a className="nx-brand" href="/" aria-label="Nexus home">
            <img src="/brand/nexus-app.svg" width="30" height="30" alt="" />
            <span>Nexus</span>
          </a>
          <nav className="nx-nav-links" aria-label="Main">
            {NAV.map((l) => (
              <a key={l.href} href={l.href}>{l.label}</a>
            ))}
            <a href={DEMO_PATH}>Live demo</a>
          </nav>
          <div className="nx-nav-end">
            <a className="nx-nav-signin" href={SIGN_IN_PATH}>Sign in</a>
            <DownloadCta size="sm" />
            <details className="nx-menu">
              <summary aria-label="Menu"><Menu size={18} aria-hidden="true" /></summary>
              <nav aria-label="Mobile">
                {NAV.map((l) => (
                  <a key={l.href} href={l.href}>{l.label}</a>
                ))}
                <a href={DEMO_PATH}>Live demo</a>
                <a href={SIGN_IN_PATH}>Sign in</a>
              </nav>
            </details>
          </div>
        </div>
      </header>

      <main id="main">
        <section id="how" className="nx-hero" aria-labelledby="hero-title">
          <div className="nx-wrap">
            <div className="nx-hero-copy">
              <a className="nx-new-pill" href="#features">
                <span className="nx-new-pill-tag">New in {LATEST_VERSION}</span>
                <span className="nx-new-pill-text">Undo any change an agent makes</span>
                <ArrowRight size={15} aria-hidden="true" />
              </a>
              <h1 id="hero-title">Every coding agent in one app, with one shared memory.</h1>
              <p className="nx-hero-lede">
                Chat with Claude Code, Codex and more in one place. They keep working while you’re away, and each starts from what
                the last one learned.
              </p>
              <CtaGroup />
            </div>
            <Pipeline />
          </div>
        </section>

        <Showcase />

        <section id="more" className="nx-section nx-more" aria-labelledby="more-title">
          <div className="nx-wrap nx-more-grid">
            <figure className="nx-film" data-film>
              <img className="nx-film-poster" src="/landing/nexus-film-poster.jpg" alt="" width="1600" height="900" loading="lazy" decoding="async" />
              {/* A silent 16 s cut loops here while in view (never with reduced motion or Save-Data). */}
              <video className="nx-film-loop" data-film-loop muted loop playsInline preload="none" aria-hidden="true" tabIndex={-1}>
                <source data-src="/landing/nexus-film.webm" type="video/webm" />
                <source data-src="/landing/nexus-film.mp4" type="video/mp4" />
              </video>
              <button type="button" className="nx-film-open" data-film-open aria-haspopup="dialog">
                <span className="nx-film-badge">
                  <span className="nx-film-disc" aria-hidden="true"><Play size={20} fill="currentColor" strokeWidth={0} /></span>
                  <span className="nx-film-cta">
                    <span>Watch the film</span>
                    <span className="nx-film-meta">{FILM_DURATION} · with music</span>
                  </span>
                </span>
              </button>
              <figcaption className="nx-sr">Product film showing chat, memory, the knowledge graph and session handoff in Nexus.</figcaption>
            </figure>
            <div className="nx-more-copy">
              <h2 id="more-title">And everything around it.</h2>
              <ul className="nx-more-list">
                {MORE.map(({ icon: Icon, t, d }) => (
                  <li key={t}>
                    <Icon size={18} strokeWidth={1.75} aria-hidden="true" />
                    <span>
                      <strong>{t}</strong>
                      <small>{d}</small>
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          </div>
        </section>

        <section id="faq" className="nx-section nx-faq-section" aria-labelledby="faq-title">
          <div className="nx-wrap nx-faq-grid">
            <h2 id="faq-title">Questions</h2>
            <div className="nx-faq">
              {FAQ.map((f) => (
                <details key={f.q}>
                  <summary>
                    <h3>{f.q}</h3>
                    <ChevronDown size={18} aria-hidden="true" />
                  </summary>
                  <p>{f.a}</p>
                </details>
              ))}
            </div>
          </div>
        </section>

        <section id="download" className="nx-final" aria-labelledby="final-title">
          <div className="nx-wrap nx-final-in">
            <img className="nx-final-mark" src="/brand/nexus-app.svg" width="56" height="56" alt="" />
            <h2 id="final-title">Give every agent the same memory.</h2>
            <p className="nx-final-lede">Free to start. Install the beta, connect your agents, and your next chat begins with what the last one learned.</p>
            <CtaGroup />
            <a className="nx-final-link" href={SIGN_UP_PATH}>
              Or create a free account <ArrowRight size={16} aria-hidden="true" />
            </a>
            <div id="mac" className="nx-mac-note">
              <p>
                <strong>On a Mac?</strong> <a href={MAC_DOWNLOAD_URL}>Download the macOS beta</a> (Nexus.dmg, Apple silicon,
                macOS 12 or later), open it and drag Nexus to Applications. It updates itself after that.
              </p>
              <p className="nx-mac-note-small">
                The beta isn't signed by Apple yet, so macOS asks once. If it says it can't verify Nexus, right-click Nexus in
                Applications and choose Open. On macOS 15 and later, open System Settings › Privacy &amp; Security and click Open
                Anyway.
              </p>
            </div>
          </div>
        </section>
      </main>

      <SiteFooter />
      <FilmPlayer />
    </div>
  )
}

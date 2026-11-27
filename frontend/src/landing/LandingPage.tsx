import {
  ArrowRight,
  ChevronDown,
  Download,
  Feather,
  Gauge,
  Maximize,
  Menu,
  Minimize,
  MousePointerClick,
  Pause,
  Play,
  Plug,
  ShieldCheck,
  Smartphone,
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
  type AgentId,
} from './content'
import { SiteFooter } from './SiteFooter'

/*
 * The public landing page. Rendered to static HTML at build time
 * (scripts/prerender.tsx); the browser only runs src/landing/client.ts,
 * never React, so everything here must work as plain markup.
 *
 * One story: every coding agent in one app, with one shared memory.
 * Hero → agents → three pillars (chat, control, memory) → film → the rest → FAQ and download.
 * The page is dark only, like the app it shows (see DESIGN.md).
 */

const agentName = (id: AgentId) => AGENTS.find((a) => a.id === id)?.name ?? id

/* ---------- the annotated file (memory section) ---------- */

type Line = { code: string; agent: AgentId; session: string; when: string; why: string }

const FILE: Line[] = [
  { code: 'export async function getToken(session: Session) {', agent: 'codex', session: 'Split auth helpers', when: '3 weeks ago', why: 'Moved token lookup out of the API client so the desktop app and the CLI share one implementation.' },
  { code: '  const token = await store.read(session.id)', agent: 'codex', session: 'Split auth helpers', when: '3 weeks ago', why: 'Reads from the shared credential store instead of each caller keeping its own copy.' },
  { code: '  if (!token) return signIn(session)', agent: 'cursor', session: 'Handle signed-out users', when: '9 days ago', why: 'Send people without a stored token to sign-in instead of throwing an unhandled error.' },
  { code: '  // Refresh early: long agent runs outlive tokens.', agent: 'claude', session: 'Stop 401 loops on long runs', when: '2 days ago', why: 'Explains the 60-second margin so the next agent doesn’t “simplify” it away.' },
  { code: '  if (token.expiresAt - Date.now() < 60_000) {', agent: 'claude', session: 'Stop 401 loops on long runs', when: '2 days ago', why: 'Refresh 60 s before expiry. Agent runs that lasted longer than a token’s life were failing mid-task with 401s.' },
  { code: '    return refresh(token, { retries: 2 })', agent: 'opencode', session: 'Retry refresh on flaky Wi-Fi', when: 'yesterday', why: 'Two retries on network errors: a single dropped request was signing people out.' },
  { code: '  }', agent: 'claude', session: 'Stop 401 loops on long runs', when: '2 days ago', why: 'Closes the early-refresh branch.' },
  { code: '  return token', agent: 'codex', session: 'Split auth helpers', when: '3 weeks ago', why: 'Valid tokens are returned unchanged.' },
  { code: '}', agent: 'codex', session: 'Split auth helpers', when: '3 weeks ago', why: 'End of getToken.' },
]
const SELECTED = 4

const KEYWORDS = /^(export|async|function|const|await|if|return)$/

function highlight(code: string): ReactNode[] {
  const out: ReactNode[] = []
  const re = /(\/\/.*$)|('[^']*')|(\b\d[\d_]*\b)|(\b[A-Za-z_]\w*\b)|(\s+|.)/g
  let m: RegExpExecArray | null
  let i = 0
  while ((m = re.exec(code))) {
    const [t, comment, str, num, word] = m
    if (comment) out.push(<span key={i++} className="t-c">{t}</span>)
    else if (str) out.push(<span key={i++} className="t-s">{t}</span>)
    else if (num) out.push(<span key={i++} className="t-n">{t}</span>)
    else if (word && KEYWORDS.test(word)) out.push(<span key={i++} className="t-k">{t}</span>)
    else if (word && /^[A-Z]/.test(word)) out.push(<span key={i++} className="t-t">{t}</span>)
    else out.push(t)
  }
  return out
}

function WhyFile() {
  const sel = FILE[SELECTED]
  return (
    <figure className="nx-why" aria-label="Example: a source file annotated by Nexus with the agent session behind each line">
      <div className="nx-why-bar">
        <span className="nx-why-file">src/auth/token.ts</span>
        <span className="nx-why-hint">Pick a line to see why</span>
        <span className="nx-label-chip">Example</span>
      </div>
      <div className="nx-why-body">
        <ol className="nx-why-code">
          {FILE.map((line, n) => (
            <li key={n}>
              <button
                type="button"
                className="nx-why-line"
                aria-pressed={n === SELECTED}
                tabIndex={n === SELECTED ? 0 : -1}
                data-agent={line.agent}
                data-agent-name={agentName(line.agent)}
                data-session={line.session}
                data-when={line.when}
                data-why={line.why}
                data-line={n + 1}
              >
                <span className="nx-why-gutter" data-agent={line.agent}>
                  <span className="nx-agent-dot" aria-hidden="true" />
                  <span className="nx-why-who">{agentName(line.agent).split(' ')[0]}</span>
                </span>
                <span className="nx-why-num" aria-hidden="true">{n + 1}</span>
                <code>{highlight(line.code)}</code>
              </button>
            </li>
          ))}
        </ol>
        <aside className="nx-why-note" aria-live="polite">
          <p className="nx-why-note-head">
            <span className="nx-mono nx-why-call">why(token.ts:<span data-why-field="line">{SELECTED + 1}</span>)</span>
            <span className="nx-why-note-agent" data-why-field="agent" data-agent={sel.agent}>
              <span className="nx-agent-dot" aria-hidden="true" />
              <span data-why-field="agent-name">{agentName(sel.agent)}</span>
            </span>
            <span className="nx-why-note-session">
              “<span data-why-field="session">{sel.session}</span>” · <span data-why-field="when">{sel.when}</span>
            </span>
          </p>
          <p className="nx-why-note-text" data-why-field="why">{sel.why}</p>
          <p className="nx-why-note-foot nx-mono">Tied to the line itself, not its number, so it follows the code when it moves.</p>
        </aside>
      </div>
    </figure>
  )
}

/* ---------- product screenshots ---------- */

// Real captures of Nexus for Windows, signed in to a demo workspace with
// fictional people and projects (never real chats). The wide shots are 5:3
// crops of the 1800x1080 window and run at the full content width; `changes`
// is a closer crop of the review panel for the side-by-side row. Each ships as
// AVIF and WebP at two widths, with a PNG fallback at the larger one.
// `focus` is the object-position used when a wide frame narrows to 4:3 on phones.
type ShotSpec = { name: string; w: number; h: number; small: number; label: string; focus: string; alt: string }

const SHOTS = {
  running: { name: 'running', w: 1300, h: 780, small: 800, label: 'Three chats at once', focus: '0% 0%', alt: 'Nexus for Windows with three chats going at once: two still working, shown by a spinner in the chat list, one waiting for approval, shown by an amber dot, while the open chat with Claude Code streams its reply after thinking for 14 seconds.' },
  chat: { name: 'chat', w: 1400, h: 840, small: 800, label: 'Chat', focus: '50% 0%', alt: 'Nexus chat with Claude Code: the request to retry failed card captures, an expanded work log with a memory search, a file read, a code search, an Explore sub-agent with its steps and findings and an Open as a chat link, then the first edits to retry.ts.' },
  changes: { name: 'changes', w: 900, h: 780, small: 800, label: 'Review changes', focus: '50% 0%', alt: 'Review changes after a reply: docs/retries.md added and src/retry.ts changed in three places, each file with its own Undo and Keep buttons and the diff of every change.' },
  memory: { name: 'memory', w: 1140, h: 684, small: 800, label: 'Memory', focus: '0% 0%', alt: 'Memory page for the checkout-service project: a project brief with key decisions (captures retry with backoff and full jitter, one Idempotency-Key per capture) and the chats they came from, search with filters, and the first of eight memories.' },
} satisfies Record<string, ShotSpec>

const WIDE_SIZES = '(min-width: 1288px) 1240px, calc(100vw - 32px)'
const SIDE_SIZES = '(min-width: 1288px) 720px, (min-width: 980px) 58vw, calc(100vw - 32px)'

function Shot({ s, sizes = WIDE_SIZES, className = '', eager = false }: { s: ShotSpec; sizes?: string; className?: string; eager?: boolean }) {
  const base = `/landing/shots/${s.name}`
  const widths = s.small === s.w ? [s.w] : [s.small, s.w]
  const set = (ext: string) => widths.map((w) => `${base}-${w}.${ext} ${w}w`).join(', ')
  return (
    <figure className={`nx-shot ${className}`} style={{ '--focus': s.focus, '--ratio': `${s.w} / ${s.h}` } as CSSProperties}>
      {/* Tapping a shot opens the full-size image, which phones can pinch-zoom. */}
      <a className="nx-shot-win" href={`${base}-${s.w}.png`} target="_blank" rel="noopener">
        <picture>
          <source type="image/avif" srcSet={set('avif')} sizes={sizes} />
          <source type="image/webp" srcSet={set('webp')} sizes={sizes} />
          <img
            src={`${base}-${s.w}.png`}
            width={s.w}
            height={s.h}
            alt={s.alt}
            loading={eager ? 'eager' : 'lazy'}
            decoding="async"
            {...(eager ? { fetchPriority: 'high' as const } : {})}
          />
        </picture>
      </a>
      <figcaption className="nx-shot-cap">
        <img src="/brand/nexus-app.svg" width="14" height="14" alt="" />
        <span>
          Nexus for Windows <span aria-hidden="true">·</span> {s.label}
        </span>
      </figcaption>
    </figure>
  )
}

/* ---------- shared bits ---------- */

function Pillar({ id, kicker, title, lede, children, className = '' }: { id: string; kicker: string; title: ReactNode; lede: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <section id={id} className={`nx-section nx-pillar ${className}`} aria-labelledby={`${id}-title`}>
      <div className="nx-wrap">
        <header className="nx-section-head">
          <p className="nx-kicker">{kicker}</p>
          <h2 id={`${id}-title`}>{title}</h2>
          <p className="nx-lede">{lede}</p>
        </header>
        {children}
      </div>
    </section>
  )
}

type Item = { t: string; d: ReactNode }

function Grid({ items, className = '' }: { items: Item[]; className?: string }) {
  return (
    <ul className={`nx-grid ${className}`}>
      {items.map((it) => (
        <li key={it.t}>
          <h3>{it.t}</h3>
          <p>{it.d}</p>
        </li>
      ))}
    </ul>
  )
}

const Kbd = ({ children }: { children: ReactNode }) => <kbd className="nx-kbd">{children}</kbd>
const Code = ({ children }: { children: ReactNode }) => <code className="nx-inline-code">{children}</code>

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
  { icon: Smartphone, t: 'Follow along on your phone', d: 'The web app shows the same chats, with the agent’s thinking live as it works.' },
  { icon: Users, t: 'Hand work to a teammate', d: 'Share a chat with its full history, or send it to another machine and carry on there. Access can be taken back.' },
  { icon: ShieldCheck, t: 'Secrets stay out', d: 'Keys and passwords are redacted before anything is saved. Your personal memory is visible only to you.' },
  { icon: Plug, t: 'Set up agents in one place', d: 'Connect the agents on your computer, edit CLAUDE.md and AGENTS.md, and add tool servers without editing JSON.' },
  { icon: Gauge, t: 'See what each agent costs', d: 'Tokens and estimated cost for every chat, agent and model, by day.' },
  { icon: Feather, t: 'Native and light', d: 'Built with WinUI 3, not Electron, so it starts fast and stays light while it sits in the tray.' },
]

const NAV = [
  { href: '#chat', label: 'Product' },
  { href: '#memory', label: 'Memory' },
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
            <a className="nx-btn nx-btn-primary nx-btn-sm" href={DOWNLOAD_URL} aria-label="Download Nexus for Windows">
              <Download size={15} aria-hidden="true" /> Download
            </a>
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
        <section className="nx-hero" aria-labelledby="hero-title">
          <div className="nx-wrap">
            <div className="nx-hero-copy">
              <a className="nx-new-pill" href="#control">
                <span className="nx-new-pill-tag">New in {LATEST_VERSION}</span>
                <span className="nx-new-pill-text">Undo any change an agent makes</span>
                <ArrowRight size={15} aria-hidden="true" />
              </a>
              <h1 id="hero-title">Every coding agent in one app, with one shared memory.</h1>
              <p className="nx-hero-lede">
                Chat with Claude Code, Codex and more in one place. They keep working while you’re away, and each starts from what
                the last one learned.
              </p>
              <div className="nx-hero-cta">
                <a className="nx-btn nx-btn-primary nx-cta-download" href={DOWNLOAD_URL}>
                  <Download size={17} aria-hidden="true" /> Download for Windows
                </a>
                <a className="nx-btn nx-btn-secondary nx-cta-demo" href={DEMO_PATH}>
                  <MousePointerClick size={16} aria-hidden="true" /> Try the live demo
                </a>
              </div>
              <p className="nx-hero-note">
                Free plan <span aria-hidden="true">·</span> Windows beta <span aria-hidden="true">·</span>{' '}
                <a href="#mac">macOS beta</a> <span aria-hidden="true">·</span> <a href={WEB_APP_PATH}>Web app</a>
              </p>
            </div>
            <Shot s={SHOTS.running} className="nx-hero-shot" eager />
          </div>
          <div className="nx-wrap">
            <div className="nx-agents">
              <p className="nx-agents-h">Works with</p>
              <ul aria-label="Supported agents">
                {AGENTS.map((a) => (
                  <li key={a.id} className="nx-agent" data-agent={a.id}>
                    <span className="nx-agent-dot" aria-hidden="true" />
                    {a.name}
                  </li>
                ))}
              </ul>
            </div>
          </div>
        </section>

        <Pillar
          id="chat"
          kicker="Chat"
          title="Pick any agent. Leave it running."
          lede="Talk to the agent that fits the job, without switching apps. Chats keep going in the background, and Nexus tells you when one needs you."
        >
          <Shot s={SHOTS.chat} />
          <Grid
            className="nx-grid-4"
            items={[
              { t: 'Keeps working when you leave', d: 'Switch chats or close the window. The reply carries on, and a notification says when it’s done.' },
              { t: 'Several chats at once', d: 'Fix a bug in one chat while another writes tests. A dot shows which one is waiting for you.' },
              { t: 'Approve right in the chat', d: 'See what the agent wants to run, then allow it once, for this chat, or always in this project.' },
              {
                t: 'Less typing',
                d: (
                  <>
                    <Kbd>@</Kbd> a file, chat or memory, <Kbd>/</Kbd> for commands, voice input, and <Kbd>Alt</Kbd>+<Kbd>Space</Kbd> to
                    ask from anywhere.
                  </>
                ),
              },
            ]}
          />
        </Pillar>

        <Pillar
          id="control"
          kicker="Control"
          className="nx-pillar-split"
          title="Keep what’s good. Undo the rest."
          lede="Every reply that touches your files gets a checkpoint, so trying an idea never costs you your work."
        >
          <div className="nx-split">
            <Grid
              className="nx-grid-stack"
              items={[
                { t: 'Review every change', d: 'See each edit the agent made, and keep or undo a whole file or a single change.' },
                {
                  t: 'Go back to any message',
                  d: (
                    <>
                      Restore the chat and your files to an earlier point, or just type <Code>/rewind</Code>.
                    </>
                  ),
                },
                {
                  t: 'Decide what runs',
                  d: (
                    <>
                      Ask every time, approve edits automatically, or give full access. Trust a command like <Code>npm test</Code> once and
                      it stops asking.
                    </>
                  ),
                },
              ]}
            />
            <Shot s={SHOTS.changes} sizes={SIDE_SIZES} className="nx-shot-tall" />
          </div>
        </Pillar>

        <Pillar
          id="memory"
          kicker="Memory"
          title="One memory for every agent."
          lede="Nexus keeps what each session decided and hands it to the next agent, whichever one you pick. No more explaining your project from scratch."
        >
          <Shot s={SHOTS.memory} />
          <div className="nx-memory">
            <div className="nx-memory-why">
              <h3 className="nx-memory-why-h">Ask why any line changed.</h3>
              <WhyFile />
            </div>
            <Grid
              className="nx-grid-stack"
              items={[
                { t: 'Saved without asking', d: 'Chats from all six agents, even ones you run in a terminal, land in one searchable timeline. Only from folders you choose.' },
                { t: 'Looked up mid-task', d: 'Agents read a short project brief first, then search memory and file history while they work.' },
                { t: 'Flags what’s out of date', d: 'When the code a memory describes changes, the memory is marked for review instead of quietly going wrong.' },
              ]}
            />
          </div>
        </Pillar>

        <section id="film" className="nx-section nx-film-section" aria-labelledby="film-title">
          <div className="nx-wrap">
            <h2 id="film-title" className="nx-film-title">See it working, start to finish.</h2>
            <figure className="nx-film" data-film>
              <img className="nx-film-poster" src="/landing/nexus-film-poster.jpg" alt="" width="1600" height="900" loading="lazy" decoding="async" />
              {/* A silent 16 s cut loops here while in view (never with reduced motion or Save-Data). */}
              <video className="nx-film-loop" data-film-loop muted loop playsInline preload="none" aria-hidden="true" tabIndex={-1}>
                <source data-src="/landing/nexus-film.webm" type="video/webm" />
                <source data-src="/landing/nexus-film.mp4" type="video/mp4" />
              </video>
              <button type="button" className="nx-film-open" data-film-open aria-haspopup="dialog">
                <span className="nx-film-badge">
                  <span className="nx-film-disc" aria-hidden="true"><Play size={22} fill="currentColor" strokeWidth={0} /></span>
                  <span className="nx-film-cta">
                    <span>Watch the film</span>
                    <span className="nx-film-meta">{FILM_DURATION} · with music</span>
                  </span>
                </span>
              </button>
              <figcaption className="nx-sr">Product film showing chat, memory, the knowledge graph and session handoff in Nexus.</figcaption>
            </figure>
          </div>
        </section>

        <section id="more" className="nx-section nx-more" aria-labelledby="more-title">
          <div className="nx-wrap">
            <h2 id="more-title">And the rest of what you’d want.</h2>
            <ul className="nx-cards">
              {MORE.map(({ icon: Icon, t, d }) => (
                <li key={t}>
                  <Icon size={20} strokeWidth={1.75} aria-hidden="true" />
                  <h3>{t}</h3>
                  <p>{d}</p>
                </li>
              ))}
            </ul>
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
            <p>Free to start. Install the beta, connect your agents, and your next chat begins with what the last one learned.</p>
            <div className="nx-hero-cta">
              <a className="nx-btn nx-btn-primary nx-cta-download" href={DOWNLOAD_URL}>
                <Download size={17} aria-hidden="true" /> Download for Windows
              </a>
              <a className="nx-btn nx-btn-secondary nx-cta-demo" href={DEMO_PATH}>
                <MousePointerClick size={16} aria-hidden="true" /> Try the live demo
              </a>
            </div>
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

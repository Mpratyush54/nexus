import { ArrowRight, ChevronDown, Download, Maximize, Menu, Minimize, Moon, MousePointerClick, Pause, Play, Sun, Volume2, VolumeX, X } from 'lucide-react'
import type { CSSProperties, ReactNode } from 'react'
import {
  AGENTS,
  DEMO_PATH,
  DOWNLOAD_URL,
  FILM_DURATION,
  FAQ,
  GITHUB_URL,
  SIGN_IN_PATH,
  SIGN_UP_PATH,
  WEB_APP_PATH,
  type AgentId,
} from './content'

/*
 * The public landing page. Rendered to static HTML at build time
 * (scripts/prerender.tsx); the browser only runs src/landing/client.ts,
 * never React, so everything here must work as plain markup.
 */

const agentName = (id: AgentId) => AGENTS.find((a) => a.id === id)?.name ?? id

function AgentTag({ id, short = false }: { id: AgentId; short?: boolean }) {
  return (
    <span className="nx-agent" data-agent={id}>
      <span className="nx-agent-dot" aria-hidden="true" />
      {short ? agentName(id).split(' ')[0] : agentName(id)}
    </span>
  )
}

/* ---------- hero: the annotated file ---------- */

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
          <p className="nx-why-note-foot nx-mono">Anchored to the line’s content hash, so it follows the code when it moves.</p>
        </aside>
      </div>
    </figure>
  )
}

/* ---------- shared bits ---------- */

function Section({ id, label, title, lede, children, className = '' }: { id: string; label: string; title: ReactNode; lede?: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <section id={id} className={`nx-section ${className}`} aria-labelledby={`${id}-title`}>
      <div className="nx-wrap">
        <header className="nx-section-head">
          <p className="nx-label">{label}</p>
          <h2 id={`${id}-title`}>{title}</h2>
          {lede && <p className="nx-lede">{lede}</p>}
        </header>
        {children}
      </div>
    </section>
  )
}

function Points({ items }: { items: { t: string; d: ReactNode }[] }) {
  return (
    <ul className="nx-points">
      {items.map((it) => (
        <li key={it.t}>
          <h3>{it.t}</h3>
          <p>{it.d}</p>
        </li>
      ))}
    </ul>
  )
}

/* ---------- product screenshots ---------- */

// Real captures of Nexus for Windows, signed in to a demo workspace with
// fictional people and projects. Every shot is cropped from a full 1800x1080
// window to the same 5:3 frame, so they line up wherever they appear: the main
// pane for whole pages, a closer crop for dialogs and panels. Each ships as
// AVIF and WebP at two widths, with a PNG fallback at the larger one.
// `focus` is the object-position used when the frame narrows to 4:3 on phones.
type ShotSpec = { name: string; w: number; h: number; small: number; label: string; focus: string; alt: string }

const SHOTS = {
  chat: { name: 'chat', w: 1400, h: 840, small: 800, label: 'Chat', focus: '50% 0%', alt: 'Nexus chat with Claude Code: the request to retry failed card captures, an expanded work log with a memory search, a file read, a code search, an Explore sub-agent with its findings and an Open sub-agent chat link, three edits, a new test file and a test run.' },
  timeline: { name: 'timeline', w: 1000, h: 600, small: 800, label: 'Timeline', focus: '0% 0%', alt: 'Nexus sidebar with search and a filter above captured chats from Claude Code, Cursor, Gemini and Codex across two projects, newest first, next to the All projects composer.' },
  memory: { name: 'memory', w: 1400, h: 840, small: 800, label: 'Memory', focus: '0% 0%', alt: 'Memory page for the checkout-service project: a project brief with key decisions and the chats they came from, filters for scope and category, and the first memory in the list.' },
  graph: { name: 'graph', w: 1400, h: 840, small: 800, label: 'Knowledge graph', focus: '0% 0%', alt: 'Nexus 3D memory graph: memories, chats and code files connected by links, with stale memories marked and a legend.' },
  agents: { name: 'agents', w: 1400, h: 840, small: 800, label: 'Agents', focus: '0% 0%', alt: 'Nexus Agents page: Claude Code, Codex and Cursor installed with the Nexus MCP server, other detected agents without it, and an Install Nexus into all agents button.' },
  share: { name: 'share', w: 1260, h: 756, small: 800, label: 'Share chat', focus: '35% 50%', alt: 'Share chat dialog: share with a person or team, keep their access up to date, and a list of who has access with a Remove button.' },
  usage: { name: 'usage', w: 1334, h: 800, small: 800, label: 'Settings · Usage', focus: '0% 0%', alt: 'Usage settings: tokens for today, the last 7 and 30 days, a tokens-per-day chart, and totals by agent with estimated cost.' },
  redaction: { name: 'redaction', w: 1140, h: 684, small: 800, label: 'Chat · redacted paste', focus: '50% 0%', alt: 'A captured chat where a pasted .env file is stored with its keys replaced by [REDACTED].' },
} satisfies Record<string, ShotSpec>

const SPLIT_SIZES = '(min-width: 1240px) 700px, (min-width: 980px) 56vw, calc(100vw - 32px)'

function Shot({ s, sizes = SPLIT_SIZES, className = '' }: { s: ShotSpec; sizes?: string; className?: string }) {
  const base = `/landing/shots/${s.name}`
  const widths = s.small === s.w ? [s.w] : [s.small, s.w]
  const set = (ext: string) => widths.map((w) => `${base}-${w}.${ext} ${w}w`).join(', ')
  return (
    <figure className={`nx-shot ${className}`} style={{ '--focus': s.focus } as CSSProperties}>
      <div className="nx-shot-win">
        <picture>
          <source type="image/avif" srcSet={set('avif')} sizes={sizes} />
          <source type="image/webp" srcSet={set('webp')} sizes={sizes} />
          <img src={`${base}-${s.w}.png`} width={s.w} height={s.h} alt={s.alt} loading="lazy" decoding="async" />
        </picture>
      </div>
      <figcaption className="nx-shot-cap">
        <img src="/brand/nexus-app.svg" width="14" height="14" alt="" />
        Nexus for Windows <span aria-hidden="true">·</span> {s.label}
      </figcaption>
    </figure>
  )
}

/* ---------- film player (a native modal <dialog>; wired up in client.ts) ---------- */

function FilmPlayer() {
  return (
    <dialog className="nx-player" data-player aria-label="Nexus product film">
      <div className="nx-player-stage" data-player-stage data-state="paused">
        <video className="nx-player-video" data-player-video data-src="/landing/nexus-film-full.mp4" playsInline preload="none" poster="/landing/nexus-film-poster.jpg" />
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

const TOOLS = [
  { name: 'memory_search', sig: '(query)', d: 'Find decisions, conventions and facts by meaning, using embeddings, not just keywords.' },
  { name: 'project_knowledge', sig: '()', d: 'The project brief and the memories an agent should read before it starts.' },
  { name: 'related_to', sig: '(file | memory)', d: 'Memories, sessions and files linked to the thing you are touching.' },
  { name: 'file_history', sig: '(path)', d: 'Every session that changed a file, newest first, with its summary.' },
  { name: 'why', sig: '(path, line)', d: 'The session and the reasoning behind one line of code.' },
  { name: 'line_history', sig: '(path, line)', d: 'Each change to a line over time, followed through moves by content hash.' },
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
            <a href="#chat">Product</a>
            <a href="#memory">Memory</a>
            <a href="#teams">Teams</a>
            <a href="#privacy">Privacy</a>
            <a href="#faq">FAQ</a>
            <a href={DEMO_PATH}>Live demo</a>
          </nav>
          <div className="nx-nav-end">
            <button type="button" className="nx-theme" data-theme-toggle aria-label="Switch colour theme">
              <Sun className="i-sun" size={17} aria-hidden="true" />
              <Moon className="i-moon" size={17} aria-hidden="true" />
            </button>
            <a className="nx-nav-signin" href={SIGN_IN_PATH}>Sign in</a>
            <a className="nx-btn nx-btn-primary nx-btn-sm" href={DOWNLOAD_URL} aria-label="Download Nexus for Windows">
              <Download size={15} aria-hidden="true" /> Download
            </a>
            <details className="nx-menu">
              <summary aria-label="Menu"><Menu size={18} aria-hidden="true" /></summary>
              <nav aria-label="Mobile">
                <a href="#chat">Product</a>
                <a href="#memory">Memory</a>
                <a href="#teams">Teams</a>
                <a href="#privacy">Privacy</a>
                <a href="#faq">FAQ</a>
                <a href={DEMO_PATH}>Live demo</a>
                <a href={SIGN_IN_PATH}>Sign in</a>
              </nav>
            </details>
          </div>
        </div>
      </header>

      <main id="main">
        <section className="nx-hero" aria-labelledby="hero-title">
          <div className="nx-wrap nx-hero-grid">
            <div className="nx-hero-copy">
              <p className="nx-label">Windows beta · free plan</p>
              <h1 id="hero-title">One memory for every coding agent you use.</h1>
              <p className="nx-hero-lede">
                Nexus is a native Windows app and a cloud server. Chat with Claude Code, Codex, Cursor, OpenCode, Gemini and Kimi in one
                place, capture every session automatically, and give every agent the same project memory, down to why a single line of
                code changed.
              </p>
              <div className="nx-hero-cta">
                <a className="nx-btn nx-btn-primary" href={DOWNLOAD_URL}>
                  <Download size={17} aria-hidden="true" /> Download for Windows
                </a>
                <a className="nx-btn nx-btn-secondary" href={DEMO_PATH}>
                  <MousePointerClick size={16} aria-hidden="true" /> Try the live demo
                </a>
                <p className="nx-hero-note">
                  Beta for Windows x64. macOS is planned. The demo runs in your browser on sample data, no account needed.{' '}
                  <a href={WEB_APP_PATH}>Open the web app</a>
                </p>
              </div>
              <ul className="nx-hero-agents" aria-label="Supported agents">
                {AGENTS.map((a) => (
                  <li key={a.id}><AgentTag id={a.id} /></li>
                ))}
              </ul>
            </div>
            <div className="nx-hero-demo">
              <WhyFile />
            </div>
          </div>
        </section>

        <section id="film" className="nx-section nx-film-section" aria-labelledby="film-title">
          <div className="nx-wrap">
            <header className="nx-section-head nx-section-head-row">
              <div>
                <p className="nx-label">see it</p>
                <h2 id="film-title">See Nexus at work.</h2>
              </div>
              <p className="nx-lede">From scattered agent chats to one shared record: the app, the memory and the handoff, shown end to end.</p>
            </header>
            <figure className="nx-film" data-film>
              <img className="nx-film-poster" src="/landing/nexus-film-poster.jpg" alt="" width="1600" height="900" loading="lazy" decoding="async" />
              {/* A silent 16 s cut loops here while in view (never with reduced motion or Save-Data). */}
              <video className="nx-film-loop" data-film-loop muted loop playsInline preload="none" aria-hidden="true" tabIndex={-1}>
                <source data-src="/landing/nexus-film.webm" type="video/webm" />
                <source data-src="/landing/nexus-film.mp4" type="video/mp4" />
              </video>
              <button type="button" className="nx-film-open" data-film-open aria-haspopup="dialog">
                <span className="nx-film-disc" aria-hidden="true"><Play size={30} fill="currentColor" strokeWidth={0} /></span>
                <span className="nx-film-cta">
                  <span>Watch the film</span>
                  <span className="nx-film-meta">{FILM_DURATION} · with sound</span>
                </span>
              </button>
              <figcaption className="nx-sr">Product film showing chat, memory, the knowledge graph and session handoff in Nexus.</figcaption>
            </figure>
            <div className="nx-film-after">
              <p>Rather click around yourself?</p>
              <a className="nx-btn nx-btn-secondary nx-btn-sm" href={DEMO_PATH}>
                Try the live demo <ArrowRight size={15} aria-hidden="true" />
              </a>
            </div>
          </div>
        </section>

        <Section
          id="chat"
          label="chat"
          title="Every agent, one window."
          lede="Talk to the agent that fits the job without switching apps. Nexus runs each one on your machine and shows its work as it happens."
          className="nx-split"
        >
          <div className="nx-split-grid">
            <Shot s={SHOTS.chat} />
            <Points
              items={[
                { t: 'Six agents, one composer', d: 'Claude Code, Codex, Cursor, OpenCode, Gemini and Kimi, with a model picker for each.' },
                { t: 'Live work log', d: 'Replies stream in with every tool call: files read, edits with their diffs, commands and results.' },
                { t: 'Sub-agent runs you can open', d: 'When an agent delegates, the sub-run gets its own log you can step into.' },
                { t: 'File references open in the app', d: 'Click a path in a reply to read the file without leaving the chat.' },
              ]}
            />
          </div>
        </Section>

        <Section
          id="capture"
          label="capture"
          title="Every session, captured without asking."
          lede="Nexus quietly records sessions from all six harnesses, including the ones you run in a terminal or another editor, into one timeline."
          className="nx-split nx-split-flip"
        >
          <div className="nx-split-grid">
            <Shot s={SHOTS.timeline} />
            <Points
              items={[
                { t: 'One searchable timeline', d: 'Fast full-text search across every agent and every project.' },
                { t: 'Filters that match how you work', d: 'Narrow by project or by agent; open any session to read it in full.' },
                { t: 'Light by design', d: 'Capture runs inside the desktop app. There is nothing to start by hand and no extra window to keep open.' },
              ]}
            />
          </div>
        </Section>

        <Section
          id="memory"
          label="memory"
          title="Memory your agents can actually use."
          lede="Notes in a wiki don’t help an agent mid-task. Nexus gives every agent the same memory through an MCP server, so a new session starts where the last one stopped."
        >
          <div className="nx-memory">
            <dl className="nx-tools" aria-label="Nexus MCP tools">
              {TOOLS.map((t) => (
                <div key={t.name}>
                  <dt><code>{t.name}<span>{t.sig}</span></code></dt>
                  <dd>{t.d}</dd>
                </div>
              ))}
            </dl>
            <Points
              items={[
                { t: 'A project brief', d: 'A short, current summary of the project that agents read first.' },
                { t: 'Staleness tracking', d: 'When the code a memory describes changes, the memory is flagged for review instead of silently going wrong.' },
                { t: 'Instruction files in sync', d: 'CLAUDE.md, AGENTS.md and the other agent instruction files stay consistent across harnesses.' },
                { t: 'Review and history', d: 'Approve, edit or retire memories, and see every version of each one.' },
              ]}
            />
          </div>
          <Shot s={SHOTS.memory} sizes="(min-width: 1240px) 1200px, calc(100vw - 32px)" className="nx-shot-wide" />
        </Section>

        <Section
          id="why"
          label="why"
          title="Know which agent changed a line, and why."
          lede="Nexus links memories, sessions and files into one code knowledge graph, and keeps a per-line history anchored to content hashes, so the answer follows the code when it moves."
        >
          <div className="nx-why-grid">
            <Shot s={SHOTS.graph} />
            <Points
              items={[
                { t: 'Code links', d: 'Every memory and session is linked to the files it touched, so related_to can walk from a file to its decisions and back.' },
                { t: 'A 3D knowledge graph', d: 'Explore the whole project in the app: zoom, search, focus on a node’s neighbours, and spot stale knowledge.' },
                { t: 'Per-line why', d: 'line_history and why tell you the agent, the session and the reasoning behind any line, even after it has moved.' },
              ]}
            />
          </div>
        </Section>

        <Section
          id="install"
          label="install"
          title="One click to wire up every harness."
          lede="Nexus finds the agents on your machine and installs its MCP server and instruction files into each of them. No config files to edit."
          className="nx-split"
        >
          <div className="nx-split-grid">
            <Shot s={SHOTS.agents} />
            <Points
              items={[
                { t: 'Detects what you have', d: 'Only the agents installed on this machine are offered.' },
                { t: 'Reversible', d: 'Remove Nexus tools from any harness from the same screen.' },
              ]}
            />
          </div>
        </Section>

        <Section
          id="teams"
          label="teams"
          title="Hand over the work, not a transcript."
          lede="Agent sessions are usually locked to one person and one machine. In Nexus they can move with the work."
          className="nx-split nx-split-flip"
        >
          <div className="nx-split-grid">
            <Shot s={SHOTS.share} />
            <Points
              items={[
                { t: 'Share sessions', d: 'Give a teammate the full conversation, tool activity and saved changes.' },
                { t: 'Teleport', d: 'Send a session to a teammate or to another of your machines and continue it there.' },
                { t: 'Organisations', d: 'Group projects and people, with roles that decide who sees what.' },
                { t: 'Grants you can take back', d: 'Every share is a grant. Revoke it and access ends.' },
              ]}
            />
          </div>
        </Section>

        <Section
          id="usage"
          label="usage"
          title="See what each agent costs."
          lede="Tokens and cost are tracked per chat, per agent and per model, so you can tell which work is worth which model."
          className="nx-split"
        >
          <div className="nx-split-grid">
            <Shot s={SHOTS.usage} />
            <Points
              items={[
                { t: 'Today, 7 and 30 days', d: 'Token totals for each window, split into input and output, with an estimated cost.' },
                { t: 'Tokens per day', d: 'A 30-day chart shows when the heavy runs happened.' },
                { t: 'By agent and by model', d: 'Cost is what the agent reported, or an estimate from list prices. Models with no known price show tokens only.' },
              ]}
            />
          </div>
        </Section>

        <Section
          id="privacy"
          label="privacy"
          title="Shared memory, private by default."
          lede="Capturing agent sessions means capturing what you paste into them. Nexus treats that carefully."
        >
          <div className="nx-privacy">
            <Shot s={SHOTS.redaction} />
            <Points
              items={[
                { t: 'Secrets redacted', d: 'Keys and passwords in captured sessions are replaced before they are saved.' },
                { t: 'Tokens masked', d: 'Access tokens show only enough to recognise them.' },
                { t: 'Personal memory stays yours', d: 'Your private memory is visible only to you. Projects and teams see only what you share.' },
                { t: 'You choose what is captured', d: 'Pick the workspaces Nexus watches and pause capture at any time.' },
              ]}
            />
          </div>
        </Section>

        <section className="nx-native" aria-labelledby="native-title">
          <div className="nx-wrap nx-native-in">
            <p className="nx-label">native</p>
            <h2 id="native-title">Built with WinUI 3. Not Electron.</h2>
            <p>
              Nexus Desktop uses the Windows UI stack directly, so it starts fast and stays light while it sits in the tray capturing
              sessions. Everything else, from memory review to team settings, is also in the web app.
            </p>
          </div>
        </section>

        <Section id="faq" label="faq" title="Questions, answered.">
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
        </Section>

        <section id="download" className="nx-final" aria-labelledby="final-title">
          <div className="nx-wrap">
            <h2 id="final-title">Give your agents a memory.</h2>
            <p>Free to start. Install the Windows beta, connect your agents, and your next session begins with everything the last one learned.</p>
            <div className="nx-hero-cta">
              <a className="nx-btn nx-btn-primary" href={DOWNLOAD_URL}>
                <Download size={17} aria-hidden="true" /> Download for Windows
              </a>
              <a className="nx-btn nx-btn-secondary" href={DEMO_PATH}>
                <MousePointerClick size={16} aria-hidden="true" /> Try the live demo
              </a>
              <a className="nx-final-link" href={SIGN_UP_PATH}>
                Create a free account <ArrowRight size={16} aria-hidden="true" />
              </a>
            </div>
          </div>
        </section>
      </main>

      <footer className="nx-foot">
        <div className="nx-wrap nx-foot-in">
          <a className="nx-brand" href="/" aria-label="Nexus home">
            <img src="/brand/nexus-app.svg" width="26" height="26" alt="" />
            <span>Nexus</span>
          </a>
          <p>Shared memory for AI coding agents.</p>
          <nav aria-label="Footer">
            <a href={DOWNLOAD_URL}>Desktop releases</a>
            <a href={WEB_APP_PATH}>Web app</a>
            <a href={DEMO_PATH}>Live demo</a>
            <a href={SIGN_IN_PATH}>Sign in</a>
            <a href={SIGN_UP_PATH}>Create account</a>
            <a href={GITHUB_URL}>GitHub</a>
          </nav>
        </div>
      </footer>
      <FilmPlayer />
    </div>
  )
}

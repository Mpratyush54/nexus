# Nexus marketing site: design system

Applies to the public site at `/` (`src/landing/`). The signed-in web app keeps its own tokens in `src/index.css`.

Direction: **a quiet dark workspace with one amber highlighter.** A near-black page that matches the app it shows, a fine grain and a warm glow for depth (never flat black, never gaudy), quiet type, and amber used only where Nexus adds something: the flow of chats into memory, a selected tab, a focus ring. The site is **dark only**: the screenshots and film are dark, so a light page around them made no sense. No stock imagery, no fake screenshots.

One story: **every coding agent in one app, with one shared memory.** Page order: hero (headline, short lede, OS-aware download + live demo) with the animated *how it works* pipeline as its visual (agents → Nexus: capture, redact, memory → what you get) → one tabbed feature showcase (Chat · Control · Memory · Live; one crisp shot per tab) → a band with the film beside six one-line extras → FAQ (six questions, two columns) → final download with the macOS note. About 4,500px at 1440.

Not based on UXKIN references (not connected); built from the existing Nexus brand mark (graphite, light stone, amber) and the app's own UI.

## Color

Tokens live on `html.nx-home, .nx` in `src/landing/site.css` as `--nx-*`, dark only (no theme toggle, no `prefers-color-scheme` switch). `html.nx-home` sets `color-scheme: dark` and the page background; `index.html` paints the same background inline so nothing flashes light before the stylesheet loads. The legal pages use the same tokens. The signed-in web app is unaffected.

| Token | Value | Role |
| --- | --- | --- |
| `--nx-paper` | `#0C0E0D` | Page background |
| `--nx-panel` / `--nx-panel-2` | `#141615` / `#1A1D1B` | Cards, pills, inline code |
| `--nx-ink` | `#ECEBE6` | Headings and body text |
| `--nx-ink-2` | `#A9ACA7` | Secondary text, ledes (8:1 on paper) |
| `--nx-ink-3` | `#8D918B` | Captions and small notes (6:1 on paper) |
| `--nx-rule` / `--nx-rule-2` | `#242725` / `#343835` | Hairlines, borders |
| `--nx-amber` | `#E8BB67` | Brand highlighter: the "why" annotation, selected line, never body text |
| `--nx-amber-ink` | `#F0C67A` | Amber text (focus ring, icons, links in the pipeline) |
| `--nx-cta` / `--nx-cta-ink` | `#ECEBE6` / `#0C0E0D` | Primary button |
| `--nx-scrim` / `--nx-veil` | translucent black | Film scrim, sticky nav and film badge |
| `--nx-band` / `--nx-glow` / `--nx-noise` | `#101211` / amber 13% / SVG grain | Section band, warm light, page grain |
| `--nx-win-*` | fixed dark | Screenshot frames |

Agent dots in mocks (decoration only, always paired with a name): Claude Code `#D9895B`, Codex `#9AA7F0`, Cursor `#BFC5C2`, OpenCode `#7FC2A6`, Gemini `#7AA8F0`, Kimi `#C59AE6`.

## Typography

- Display: **Bricolage Grotesque** (variable, self-hosted, latin), weight 620, tracking -0.035em. Only h1, h2 and the final CTA.
- Body: **IBM Plex Sans** 400/500/600 (self-hosted). Shared with the app.
- Utility: **IBM Plex Mono** 400/500: pipeline column heads, captions, the version pill.
- All `font-display: swap`; the display face is preloaded.

Scale (px, line height): 14/1.5 mono label · 16/1.6 body · 18/1.6 lede · 22/1.3 h3 · clamp(32,4.4vw,56)/1.04 h2 · clamp(44,7.6vw,104)/0.95 h1. Body measure 60-70ch.

## Spacing and layout

4px base: 4, 8, 12, 16, 24, 32, 48, 64, 96, 128. Content width 1240px with a 24px gutter (16px under 600px). Sections are separated by 120px (96px tablet, 72px phone) of space; the film band sits on `--nx-band` between hairlines. No eyebrow labels on sections; tab names and the pipeline's column heads do that job.

## Shape and depth

Radius: 6px controls, 10px panels, 14px app windows, 20px for the two big containers (pipeline, tab panel), pills for tabs. One window shadow (`--shadow-win`). Depth comes from `--nx-noise` (SVG grain), `--nx-glow` radial light behind the hero and final CTA, a faint grid in the hero that fades out, and the band behind the film.

## Components

- Buttons: primary (solid `--nx-cta`), secondary (1px rule, transparent), quiet (text with underline on hover). Height 44px, 6px radius. Focus: 2px `--nx-amber-ink` outline, 3px offset. Disabled is not used on the site.
- Links: ink, underline offset 3px; descriptive text, never "click here".
- Nav: sticky, translucent dark veil with a bottom rule once scrolled; How it works, Features, FAQ, Live demo, Sign in, and the OS-aware Download. Under 860px the links move into a `<details>` menu (no JS).
- Downloads follow the visitor: `src/landing/os.ts` (userAgentData, then userAgent; iPadOS counts as a tablet when it has touch) sets `html[data-os]` to win, mac, mobile or other, and elements with `data-for` show only for their OS. Windows: Download for Windows + live demo. Mac: Download for Mac (the DMG) + live demo. Phones, tablets and Linux: the live demo is primary, with links to both downloads. No JS shows the Windows set. Primary buttons share one min-width so the swap never moves the layout.
- Pipeline: HTML with SVG wires (stretched with `preserveAspectRatio=none` and non-scaling strokes) and CSS-animated amber dashes, the three stages pulsing in turn. Under 900px it runs top to bottom with short animated connectors. Static under reduced motion; a hidden figcaption describes it for screen readers.
- Feature tabs: an ARIA tablist (arrows, Home, End). With JS (`html.nx-js`, set in the head) the panels share one grid cell so switching only cross-fades; on narrow screens only the active panel shows; without JS every panel shows in order.
- Screenshots: real captures of the web app on the `/demo` sample data (a fictional team), taken in headless Chrome at 2x (the phone at 3x) and cropped to the content. Never shown wider than half their pixel width (`--max`), served as AVIF/WebP at full and half size with a PNG fallback; captioned "Nexus on the web · <screen>". Phones crop wide shots square to their top-left so text stays legible; tap opens the full PNG.
- Brand mark: the desktop app icon itself (`public/brand/nexus-app.svg`, dark tile + mark, same artwork as `Nexus.ico`) everywhere: landing header/footer, portal sidebar, favicon, PWA icons, OG image.
- Film: poster under a dark scrim, with the play button and "Watch the film · 1:26" on a solid badge so it is legible over any frame; it opens the full film with sound in a modal `<dialog>` player (custom controls, keyboard, Esc closes, focus restored). A muted 16 s cut loops behind the poster while in view, except with reduced motion or Save-Data.
- FAQ: native `<details>`; content is also emitted as FAQPage JSON-LD from the same data.
- Ambient background (`src/landing/ambient.ts`): Nexus at work, drawn in faint ghosted app cards round the hero headline, each placed from the measured text boxes so it never sits under text. A 16 s story plays clockwise: a chat with one agent ("cap retries at 3, backoff 2s") and "decision captured" (or a key masked as "secret redacted"); the decision travels over the top as a chip into the Nexus shared-memory card; memory flows down to a new session with another agent ("context loaded · from Claude Code") whose reply already uses it; the code it writes travels under the buttons to a file card whose line carries its why ("why ← chat with Codex · decision from Claude Code"), traced back to the first chat (one story shows Undo instead). Source and target rotate so all six agents take both roles; the memory card keeps earlier decisions. Where the sides are too narrow (phones, tablets) one strip under the buttons tells the same story: agent → Nexus → agent, with a one-line caption. Canvas 2D inside the hero at 30 fps, DPR capped at 2 (1.5 on low-power devices), its own chunk loaded once the page has loaded and the browser is idle; paused while the tab is hidden, the hero is off screen or the film is open. Reduced motion: one still frame with every card filled in. No JS, or below the hero: a still CSS lattice of points at the sides (`.nx-ambient`). `aria-hidden`, no pointer events.

## Imagery and icons

Lucide icons at 16-18px, 1.75 stroke. Product visuals are real screenshots only (never illustrations), with alt text that describes what the screen shows, and only demo data. The pipeline is a diagram, not a screenshot.

## Motion

The ambient "Nexus at work" scene in the hero (about 30 fps, see Components), a short staggered rise for the hero copy and pipeline on load, the pipeline's flowing wires and stage pulse, the tab cross-fade, a one-time rise as sections scroll into view, and the film loop. Everything honours `prefers-reduced-motion` (no animation, no autoplay, nothing hidden).

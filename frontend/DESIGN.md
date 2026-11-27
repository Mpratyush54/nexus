# Nexus marketing site: design system

Applies to the public site at `/` (`src/landing/`). The signed-in web app keeps its own tokens in `src/index.css`.

Direction: **an annotated source file.** Nexus's sharpest idea is that every line of code can answer "which agent changed this, and why". The site borrows the look of an editor gutter and a code-review margin: a near-black page that matches the app windows it shows, quiet type, and one amber highlighter used only where Nexus adds an annotation. The site is **dark only**: the screenshots, film and code card are dark, so a light page around them made no sense. No decorative gradients (the film scrim is the one functional overlay), no stock imagery.

One story: **every coding agent in one app, with one shared memory.** Page order: hero (headline, short lede, Download + live demo, one real screenshot) → agent strip (the six agents, named once) → three pillars, each one large screenshot with a compact grid folded under it (Chat: full-width shot + 4-up grid; Control: notes beside a tall crop; Memory: full-width shot, then the annotated file beside the notes) → film → "the rest" as six compact cards → FAQ (two columns) → final download with the macOS note.

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
| `--nx-amber-ink` | `#F0C67A` | Amber text (pillar kicker, focus ring, icons) |
| `--nx-cta` / `--nx-cta-ink` | `#ECEBE6` / `#0C0E0D` | Primary button |
| `--nx-scrim` / `--nx-veil` | translucent black | Film scrim, sticky nav and film badge |
| `--nx-win-*`, `--code-*` | fixed dark | The annotated file card and its syntax colours |

Agent dots in mocks (decoration only, always paired with a name): Claude Code `#D9895B`, Codex `#9AA7F0`, Cursor `#BFC5C2`, OpenCode `#7FC2A6`, Gemini `#7AA8F0`, Kimi `#C59AE6`.

## Typography

- Display: **Bricolage Grotesque** (variable, self-hosted, latin), weight 620, tracking -0.035em. Only h1, h2 and the final CTA.
- Body: **IBM Plex Sans** 400/500/600 (self-hosted). Shared with the app.
- Utility: **IBM Plex Mono** 400/500: section labels, tool names, gutter, data.
- All `font-display: swap`; the display face is preloaded.

Scale (px, line height): 14/1.5 mono label · 16/1.6 body · 18/1.6 lede · 22/1.3 h3 · clamp(32,4.4vw,56)/1.04 h2 · clamp(44,7.6vw,104)/0.95 h1. Body measure 60-70ch.

## Spacing and layout

4px base: 4, 8, 12, 16, 24, 32, 48, 64, 96, 128. Content width 1240px with a 24px gutter (16px under 600px). Sections are separated by 128px (96px tablet, 80px phone) of space, not rules. Only the three pillars carry a kicker: one plain word (Chat, Control, Memory) in amber sans, no mono label or square. Vary the rhythm: never repeat the same head + list + shot template twice in a row.

## Shape and depth

Radius: 6px controls, 10px panels, 14px app windows. One shadow, only on app windows: `0 30px 60px -30px rgb(0 0 0 / .45)`. Everything else is flat with 1px rules.

## Components

- Buttons: primary (solid `--nx-cta`), secondary (1px rule, transparent), quiet (text with underline on hover). Height 44px, 6px radius. Focus: 2px `--nx-amber-ink` outline, 3px offset. Disabled is not used on the site.
- Links: ink, underline offset 3px; descriptive text, never "click here".
- Nav: sticky, translucent dark veil with a bottom rule once scrolled; Product, Memory, FAQ, Live demo, Sign in, Download. Under 860px the links move into a `<details>` menu (no JS).
- App windows: real screenshots of the WinUI app (`public/landing/shots/`), captured from an isolated demo profile signed in to a local server with fictional people and projects. Few and large: four shots on the page (hero `running`, `chat`, `changes`, `memory`). Wide shots are 5:3 crops of the 1800x1080 window shown at the full content width; `changes` is a closer crop of the review panel (900x780) for the side-by-side row. All sit in the same frame (10px radius, hairline, window shadow) with a "Nexus for Windows · <screen>" caption; phones narrow wide frames to 4:3 around each shot's focus point. Only the hero shot loads eagerly.
- Brand mark: the desktop app icon itself (`public/brand/nexus-app.svg`, dark tile + mark, same artwork as `Nexus.ico`) everywhere: landing header/footer, portal sidebar, favicon, PWA icons, OG image.
- Film: poster under a dark scrim, with the play button and "Watch the film · 1:26" on a solid badge so it is legible over any frame; it opens the full film with sound in a modal `<dialog>` player (custom controls, keyboard, Esc closes, focus restored). A muted 16 s cut loops behind the poster while in view, except with reduced motion or Save-Data. The annotated file (in the Memory pillar) is the one HTML recreation and keeps its "Example" label.
- FAQ: native `<details>`; content is also emitted as FAQPage JSON-LD from the same data.

## Imagery and icons

Lucide icons at 16-18px, 1.75 stroke. Product visuals are real screenshots of the Windows app in its dark theme, served as `<picture>` with AVIF and WebP at two widths and a PNG fallback, explicit width and height, lazy-loaded, with alt text that describes what the screen shows. They contain only demo data. The film lazy-loads and falls back to its poster or a drawn panel.

## Motion

A short staggered rise for the hero copy and window on load, a one-time rise as shots and feature notes scroll into view, the annotated file's selected line, and the film loop. Everything honours `prefers-reduced-motion` (no animation, no autoplay, nothing hidden).

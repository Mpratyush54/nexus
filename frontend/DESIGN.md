# Nexus marketing site: design system

Applies to the public site at `/` (`src/landing/`). The signed-in web app keeps its own tokens in `src/index.css`.

Direction: **an annotated source file.** Nexus's sharpest idea is that every line of code can answer "which agent changed this, and why". The site borrows the look of an editor gutter and a code-review margin: quiet neutral paper, dense mono labels, and one amber highlighter used only where Nexus adds an annotation. Product windows are shown dark, as the Windows app ships. No gradients, no glass, no stock imagery.

Not based on UXKIN references (not connected); built from the existing Nexus brand mark (graphite, light stone, amber) and the app's own UI.

## Color

Tokens live on `.nx` in `src/landing/site.css` as `--nx-*`. Light is the default; dark follows the OS and can be set with the toggle (`html[data-theme]`).

| Token | Light | Dark | Role |
| --- | --- | --- | --- |
| `--nx-paper` | `#F4F5F2` | `#0E100F` | Page background |
| `--nx-panel` | `#FFFFFF` | `#161817` | Raised surfaces: FAQ, tables, cards |
| `--nx-ink` | `#141617` | `#ECEBE6` | Headings and body text |
| `--nx-ink-2` | `#4B5053` | `#A6A9A5` | Secondary text, captions (AA on paper and panel) |
| `--nx-rule` | `#D9DBD5` | `#2A2D2B` | Hairlines, borders |
| `--nx-amber` | `#E8BB67` | `#E8BB67` | Brand highlighter: the "why" annotation, selected line, never body text |
| `--nx-amber-ink` | `#7A4D00` | `#F0C67A` | Amber text on paper (labels, focus ring) |
| `--nx-cta` / `--nx-cta-ink` | `#141617` / `#F4F5F2` | `#ECEBE6` / `#0E100F` | Primary button |
| `--nx-win-*` | fixed dark | fixed dark | Recreated app windows (`#121413` base, `#1B1E1C` raised, `#E6E4DE` text, `#9EA29D` dim) |

Agent dots in mocks (decoration only, always paired with a name): Claude Code `#D9895B`, Codex `#9AA7F0`, Cursor `#BFC5C2`, OpenCode `#7FC2A6`, Gemini `#7AA8F0`, Kimi `#C59AE6`.

## Typography

- Display: **Bricolage Grotesque** (variable, self-hosted, latin), weight 620, tracking -0.035em. Only h1, h2 and the final CTA.
- Body: **IBM Plex Sans** 400/500/600 (self-hosted). Shared with the app.
- Utility: **IBM Plex Mono** 400/500: section labels, tool names, gutter, data.
- All `font-display: swap`; the display face is preloaded.

Scale (px, line height): 14/1.5 mono label · 16/1.6 body · 18/1.6 lede · 22/1.3 h3 · clamp(32,4.4vw,56)/1.04 h2 · clamp(44,7.6vw,104)/0.95 h1. Body measure 60-70ch.

## Spacing and layout

4px base: 4, 8, 12, 16, 24, 32, 48, 64, 96, 128. Content width 1240px with a 24px gutter (16px under 600px). Sections are separated by 96-128px and a hairline rule; each opens with a mono label naming what the section is about (`chat`, `capture`, `memory`…), mirroring the MCP tool names.

## Shape and depth

Radius: 6px controls, 10px panels, 14px app windows. One shadow, only on app windows: `0 30px 60px -30px rgb(0 0 0 / .45)`. Everything else is flat with 1px rules.

## Components

- Buttons: primary (solid `--nx-cta`), secondary (1px rule, transparent), quiet (text with underline on hover). Height 44px, 6px radius. Focus: 2px `--nx-amber-ink` outline, 3px offset. Disabled is not used on the site.
- Links: ink, underline offset 3px; descriptive text, never "click here".
- Nav: sticky, paper background with a bottom rule once scrolled. Under 860px the links move into a `<details>` menu (no JS).
- App windows: real screenshots of the WinUI app (`public/landing/shots/`), captured from an isolated demo profile signed in to a local server with fictional people and projects. 14px radius and the window shadow. The hero's annotated file is the one HTML recreation and keeps its "Example" label.
- FAQ: native `<details>`; content is also emitted as FAQPage JSON-LD from the same data.

## Imagery and icons

Lucide icons at 16-18px, 1.75 stroke. Product visuals are real screenshots of the Windows app in its dark theme, served as `<picture>` with AVIF and WebP at two widths and a PNG fallback, explicit width and height, lazy-loaded, with alt text that describes what the screen shows. They contain only demo data. The film lazy-loads and falls back to its poster or a drawn panel.

## Motion

Only two moments: the hero gutter's selected line, and the film. Everything honours `prefers-reduced-motion` (no autoplay, no transitions).

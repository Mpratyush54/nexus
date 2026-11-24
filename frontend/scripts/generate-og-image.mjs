// Renders public/og/nexus-og.png (1200x630) for Open Graph / Twitter cards.
// Uses system fonts (Segoe UI / Consolas on Windows); re-run with `npm run og:build`.
import { Resvg } from '@resvg/resvg-js'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const mark = readFileSync(path.join(root, 'public', 'brand', 'nexus-mark.svg'), 'utf8')
  .replace(/<\?xml[^>]*>/, '')
  .replace('<svg', '<svg x="72" y="64" width="64" height="64"')

const agents = [
  ['Claude Code', '#D9895B'], ['Codex', '#9AA7F0'], ['Cursor', '#BFC5C2'],
  ['OpenCode', '#7FC2A6'], ['Gemini', '#7AA8F0'], ['Kimi', '#C59AE6'],
]
let x = 72
const agentSvg = agents.map(([name, color]) => {
  const w = name.length * 13.2 + 34
  const s = `<circle cx="${x + 6}" cy="548" r="6" fill="${color}"/><text x="${x + 20}" y="555" font-family="Consolas" font-size="22" fill="#D6D4CE">${name}</text>`
  x += w
  return s
}).join('')

const lines = [
  ['CC', '#D9895B', 'if (expiresAt - now &lt; 60_000) {', true],
  ['OC', '#7FC2A6', '  return refresh(token)', false],
]
const code = lines.map(([who, color, text, sel], i) => {
  const y = 418 + i * 46
  return `${sel ? `<rect x="700" y="${y - 30}" width="428" height="44" fill="#E8BB67" fill-opacity=".14"/><rect x="700" y="${y - 30}" width="4" height="44" fill="#E8BB67"/>` : ''}
  <circle cx="726" cy="${y - 8}" r="5" fill="${color}"/><text x="738" y="${y}" font-family="Consolas" font-size="18" fill="#9EA29D">${who}</text>
  <text x="780" y="${y}" xml:space="preserve" font-family="Consolas" font-size="18" fill="#E6E4DE">${text}</text>`
}).join('')

const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630">
  <rect width="1200" height="630" fill="#121413"/>
  <g stroke="#ffffff" stroke-opacity=".035">${Array.from({ length: 25 }, (_, i) => `<line x1="${i * 50}" y1="0" x2="${i * 50}" y2="630"/>`).join('')}</g>
  <rect x="72" y="64" width="64" height="64" rx="14" fill="#171918"/>
  ${mark}
  <text x="152" y="108" font-family="Segoe UI" font-weight="600" font-size="34" fill="#ECEBE6">Nexus</text>
  <text x="72" y="236" font-family="Segoe UI" font-weight="700" font-size="76" letter-spacing="-2.5" fill="#ECEBE6">One memory for</text>
  <text x="72" y="322" font-family="Segoe UI" font-weight="700" font-size="76" letter-spacing="-2.5" fill="#ECEBE6">every coding agent.</text>
  <text x="72" y="392" font-family="Segoe UI" font-size="26" fill="#A6A9A5">Native Windows app · shared MCP memory</text>
  <text x="72" y="430" font-family="Segoe UI" font-size="26" fill="#A6A9A5">· per-line “why” for every change</text>
  <rect x="700" y="372" width="428" height="104" rx="12" fill="#1B1E1C" stroke="#2D312E"/>
  ${code}
  <rect x="72" y="500" width="1056" height="1" fill="#2D312E"/>
  ${agentSvg}
</svg>`

const png = new Resvg(svg, { fitTo: { mode: 'width', value: 1200 }, font: { loadSystemFonts: true, defaultFontFamily: 'Segoe UI' } }).render().asPng()
mkdirSync(path.join(root, 'public', 'og'), { recursive: true })
writeFileSync(path.join(root, 'public', 'og', 'nexus-og.png'), png)
console.log(`Built public/og/nexus-og.png (${(png.length / 1024).toFixed(0)} kB)`)

import { Resvg } from '@resvg/resvg-js'
import { readFileSync, writeFileSync, mkdirSync, copyFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const publicDir = path.join(root, 'public')
const windowsAssets = path.resolve(root, '..', 'desktop', 'windows', 'Assets')
mkdirSync(windowsAssets, { recursive: true })
const source = readFileSync(path.join(publicDir, 'brand', 'nexus-mark.svg'), 'utf8')
const appSvg = source.replace('<title>', '<rect width="64" height="64" rx="15" fill="#171918"/><g transform="translate(5 5) scale(.84375)"><title>').replace('</svg>', '</g></svg>')
const sizes = [16, 24, 32, 48, 64, 128, 256]
const pngs = sizes.map(size => new Resvg(appSvg, { fitTo: { mode: 'width', value: size } }).render().asPng())
const entries = Buffer.alloc(6 + sizes.length * 16)
entries.writeUInt16LE(1, 2)
entries.writeUInt16LE(sizes.length, 4)
let offset = entries.length
for (let i = 0; i < sizes.length; i++) {
  const entry = 6 + i * 16
  entries[entry] = sizes[i] === 256 ? 0 : sizes[i]
  entries[entry + 1] = entries[entry]
  entries.writeUInt16LE(1, entry + 4)
  entries.writeUInt16LE(32, entry + 6)
  entries.writeUInt32LE(pngs[i].length, entry + 8)
  entries.writeUInt32LE(offset, entry + 12)
  offset += pngs[i].length
}
writeFileSync(path.join(windowsAssets, 'Nexus.ico'), Buffer.concat([entries, ...pngs]))
for (const size of [192, 512]) {
  const png = new Resvg(appSvg, { fitTo: { mode: 'width', value: size } }).render().asPng()
  writeFileSync(path.join(publicDir, `pwa-${size}.png`), png)
  if (size === 512) writeFileSync(path.join(windowsAssets, 'Nexus.png'), png)
}
writeFileSync(path.join(publicDir, 'favicon.svg'), appSvg)
copyFileSync(path.join(windowsAssets, 'Nexus.ico'), path.join(publicDir, 'brand', 'nexus.ico'))
console.log('Built Nexus brand assets: SVG, multi-resolution Windows ICO, tray PNG and PWA icons.')

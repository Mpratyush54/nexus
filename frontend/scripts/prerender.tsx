/*
 * Post-build: render the landing page to static HTML inside dist/index.html so
 * crawlers and first paint get real content without running JavaScript.
 * Also adds the landing stylesheet, font preload and JSON-LD.
 */
import { existsSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { DESCRIPTION, DOWNLOAD_URL, FAQ, GITHUB_URL, SITE_URL } from '../src/landing/content'
import { LandingPage } from '../src/landing/LandingPage'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const dist = path.join(root, 'dist')
const htmlPath = path.join(dist, 'index.html')
const manifestPath = path.join(dist, '.vite', 'manifest.json')

type Chunk = { file: string; css?: string[]; imports?: string[]; isDynamicEntry?: boolean }
const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as Record<string, Chunk>
const boot = manifest['src/landing/boot.ts']
if (!boot?.css?.length) throw new Error('prerender: landing boot chunk or its CSS is missing from the Vite manifest')

const fontFile = readdirSync(path.join(dist, 'assets')).find((f) => /^bricolage-grotesque-latin-wght-normal.*\.woff2$/.test(f))
if (!fontFile) throw new Error('prerender: display font not found in dist/assets')

const head = [
  `<link rel="preload" href="/assets/${fontFile}" as="font" type="font/woff2" crossorigin>`,
  ...boot.css.map((c) => `<link rel="stylesheet" href="/${c}">`),
  `<link rel="modulepreload" href="/${boot.file}">`,
]

const org = { '@type': 'Organization', '@id': `${SITE_URL}/#org`, name: 'Nexus', url: `${SITE_URL}/`, logo: `${SITE_URL}/pwa-512.png`, sameAs: [GITHUB_URL] }
const jsonLd = {
  '@context': 'https://schema.org',
  '@graph': [
    org,
    { '@type': 'WebSite', '@id': `${SITE_URL}/#website`, url: `${SITE_URL}/`, name: 'Nexus', publisher: { '@id': org['@id'] } },
    {
      '@type': 'SoftwareApplication',
      name: 'Nexus',
      description: DESCRIPTION,
      url: `${SITE_URL}/`,
      downloadUrl: DOWNLOAD_URL,
      operatingSystem: 'Windows',
      applicationCategory: 'DeveloperApplication',
      image: `${SITE_URL}/og/nexus-og.png`,
      offers: { '@type': 'Offer', price: '0', priceCurrency: 'USD', description: 'Free plan' },
      publisher: { '@id': org['@id'] },
    },
    {
      '@type': 'FAQPage',
      mainEntity: FAQ.map((f) => ({ '@type': 'Question', name: f.q, acceptedAnswer: { '@type': 'Answer', text: f.a } })),
    },
  ],
}
// Keep "</script>" sequences out of the inline JSON.
head.push(`<script type="application/ld+json">${JSON.stringify(jsonLd).replace(/</g, '\u003c')}</script>`)

const markup = renderToStaticMarkup(createElement(LandingPage))
let html = readFileSync(htmlPath, 'utf8')
if (!html.includes('<div id="root"></div>')) throw new Error('prerender: #root placeholder not found')
html = html.replace('</head>', `    ${head.join('\n    ')}\n  </head>`).replace('<div id="root"></div>', `<div id="root">${markup}</div>`)
writeFileSync(htmlPath, html)

// The manifest is a build artefact only; don't publish it.
if (existsSync(path.dirname(manifestPath))) rmSync(path.dirname(manifestPath), { recursive: true, force: true })
console.log(`prerender: wrote ${path.relative(root, htmlPath)} (${(html.length / 1024).toFixed(1)} kB)`)

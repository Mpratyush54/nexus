/*
 * Post-build: render the landing page to static HTML inside dist/index.html so
 * crawlers and first paint get real content without running JavaScript.
 * Also adds the landing stylesheet, font preload and JSON-LD, and writes the
 * static legal pages (dist/privacy.html, dist/terms.html).
 */
import { existsSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { DESCRIPTION, DOWNLOAD_URL, FAQ, GITHUB_URL, SITE_URL } from '../src/landing/content'
import { LandingPage } from '../src/landing/LandingPage'
import { LEGAL_META, LegalPage, type LegalKind } from '../src/landing/legal'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const dist = path.join(root, 'dist')
const htmlPath = path.join(dist, 'index.html')
const shellPath = path.join(dist, 'spa.html')
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
// Keep the plain SPA shell (empty #root, no landing CSS or markup) for app
// routes. deploy/deploy-frontend.sh uploads it under each client route and as
// the S3 error document, so /app, /login, ... never carry the landing page.
writeFileSync(shellPath, html)
html = html.replace('</head>', `    ${head.join('\n    ')}\n  </head>`).replace('<div id="root"></div>', `<div id="root">${markup}</div>`)
writeFileSync(htmlPath, html)

// Static legal pages: the landing stylesheet and its small script (theme
// toggle only here), no React. deploy/deploy-frontend.sh uploads each one
// under its extensionless path (/privacy, /terms).
const esc = (t: string) => t.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;')
const legalPaths: string[] = []
for (const kind of ['privacy', 'terms'] as LegalKind[]) {
  const meta = LEGAL_META[kind]
  const url = `${SITE_URL}${meta.path}`
  const page = `<!doctype html>
<html lang="en" class="nx-home">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover" />
    <title>${esc(meta.title)}</title>
    <meta name="description" content="${esc(meta.description)}" />
    <link rel="canonical" href="${url}" />
    <meta name="robots" content="index, follow" />
    <meta name="theme-color" content="#f4f5f2" media="(prefers-color-scheme: light)" />
    <meta name="theme-color" content="#0e100f" media="(prefers-color-scheme: dark)" />
    <meta name="color-scheme" content="light dark" />
    <link rel="icon" type="image/svg+xml" href="/favicon.svg" />
    <link rel="icon" type="image/x-icon" href="/brand/nexus.ico" sizes="any" />
    <link rel="apple-touch-icon" href="/pwa-192.png" />
    <meta property="og:type" content="website" />
    <meta property="og:site_name" content="Nexus" />
    <meta property="og:url" content="${url}" />
    <meta property="og:title" content="${esc(meta.title)}" />
    <meta property="og:description" content="${esc(meta.description)}" />
    <meta property="og:image" content="${SITE_URL}/og/nexus-og.png" />
    <script>
      (function () {
        var t = null
        try { t = localStorage.getItem('nx-theme') } catch (e) {}
        if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t
      })()
    </script>
    ${head.filter((h) => !h.includes('ld+json')).join('\n    ')}
  </head>
  <body>
    <!-- OWNER REVIEW REQUIRED: this ${meta.heading} is a plain-language draft written from what the code does. The owner must review the text (and have it checked if needed) before launch. Source: frontend/src/landing/legal.tsx -->
    ${renderToStaticMarkup(createElement(LegalPage, { kind }))}
    <script type="module" src="/${boot.file}"></script>
  </body>
</html>
`
  const out = path.join(dist, `${kind}.html`)
  writeFileSync(out, page)
  legalPaths.push(path.relative(root, out))
}

// The manifest is a build artefact only; don't publish it.
if (existsSync(path.dirname(manifestPath))) rmSync(path.dirname(manifestPath), { recursive: true, force: true })
console.log(`prerender: wrote ${path.relative(root, htmlPath)} (${(html.length / 1024).toFixed(1)} kB), ${path.relative(root, shellPath)} and ${legalPaths.join(', ')}`)

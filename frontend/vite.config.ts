import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { execSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'

const root = path.dirname(fileURLToPath(import.meta.url))

// The version error reports carry. CI can set VITE_APP_VERSION; otherwise
// package.json's version (when it is a real one) plus the git commit.
function buildVersion(): string {
  const pkg = JSON.parse(readFileSync(path.join(root, 'package.json'), 'utf8')) as { version?: string }
  let commit = ''
  try {
    commit = execSync('git rev-parse --short HEAD', { cwd: root, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim()
  } catch {
    // Not a git checkout (e.g. a source tarball): the package version alone.
  }
  const base = pkg.version && pkg.version !== '0.0.0' ? pkg.version : 'web'
  return commit ? `${base}+${commit}` : base
}
// Vite exposes VITE_* variables from the environment as import.meta.env.*.
process.env.VITE_APP_VERSION ||= buildVersion()
// Dev proxy target. Defaults to the hosted API; point it at a local server with
// NEXUS_API_PROXY=http://localhost:8080 npm run dev
const apiTarget = process.env.NEXUS_API_PROXY || 'https://api-nexus.pratyushes.dev'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(root, 'src'),
    },
  },
  build: {
    // scripts/prerender.tsx reads this to link the landing CSS; it deletes it afterwards.
    manifest: true,
    rolldownOptions: {
      output: {
        // Big vendors get their own long-lived chunks, so a page change does not
        // re-download React or the markdown stack. (codeSplitting.groups is
        // Rolldown's replacement for Rollup's manualChunks.) The landing page
        // imports none of these, so it stays React-free.
        codeSplitting: {
          groups: [
            { name: 'react', test: /node_modules[\\/](react|react-dom|scheduler|react-router|react-router-dom|cookie|set-cookie-parser)[\\/]/, priority: 30 },
            { name: 'query', test: /node_modules[\\/]@tanstack[\\/]/, priority: 20 },
            { name: 'motion', test: /node_modules[\\/](framer-motion|motion-dom|motion-utils)[\\/]/, priority: 20 },
            { name: 'icons', test: /node_modules[\\/]lucide-react[\\/]/, priority: 20 },
            {
              name: 'markdown',
              test: /node_modules[\\/](react-markdown|remark-[^\\/]+|micromark[^\\/]*|mdast-[^\\/]+|hast-[^\\/]+|unist-[^\\/]+|unified|vfile[^\\/]*|property-information|space-separated-tokens|comma-separated-tokens|decode-named-character-reference|character-entities[^\\/]*|html-url-attributes|trim-lines|devlop|bail|is-plain-obj|trough|zwitch|ccount|longest-streak|markdown-table|estree-util-[^\\/]+|style-to-[^\\/]+|inline-style-parser|extend)[\\/]/,
              priority: 20,
            },
          ],
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: apiTarget,
        changeOrigin: true,
        rewrite: (p) => p.replace(/^\/api/, ''),
      },
      '/ws': {
        target: apiTarget,
        changeOrigin: true,
        ws: true,
      },
    },
  },
})

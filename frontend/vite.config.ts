import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'

const root = path.dirname(fileURLToPath(import.meta.url))
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

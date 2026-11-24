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

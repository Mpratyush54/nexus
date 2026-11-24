import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import './index.css'
import { registerServiceWorker } from './lib/pwa'
import { initTheme } from './lib/theme'
import { dismissBootSplash } from './lib/boot-splash'
import { installGlobalErrorHandlers } from './lib/diagnostics'

// Uncaught errors and unhandled rejections become problem reports (unless
// turned off in Settings). The /demo boot never installs this.
installGlobalErrorHandlers()

// Fonts are self-hosted (src/fonts.css, pulled in by index.css).
document.documentElement.classList.remove('nx-home')
document.documentElement.classList.add('nx-app')
// The portal keeps its own theme preference (system, light or dark).
initTheme()
const container = document.getElementById('root')!
// index.html carries the prerendered landing page; the app replaces it.
container.textContent = ''
createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
dismissBootSplash()

void registerServiceWorker()

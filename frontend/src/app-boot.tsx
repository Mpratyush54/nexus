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

const APP_FONTS =
  'https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=IBM+Plex+Sans:wght@400;500;600;700&family=Syne:wght@600;700;800&display=swap'

// The web app's fonts load only here, so the landing page never waits on them.
if (!document.querySelector(`link[href="${APP_FONTS}"]`)) {
  const link = document.createElement('link')
  link.rel = 'stylesheet'
  link.href = APP_FONTS
  document.head.appendChild(link)
}

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

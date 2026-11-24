/*
 * /demo: the real web app on sample data. main.tsx loads this chunk only for
 * /demo and /demo/*; the mocks go in before any app module runs, and nothing
 * here is imported by the normal app routes.
 */
import { installDemoMocks } from './mock'
import { dismissBootSplash } from '@/lib/boot-splash'

installDemoMocks()

const APP_FONTS =
  'https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=IBM+Plex+Sans:wght@400;500;600;700&family=Syne:wght@600;700;800&display=swap'

async function boot() {
  const [{ StrictMode }, { createRoot }, { default: App }, { DemoBanner }, { initTheme }] = await Promise.all([
    import('react'),
    import('react-dom/client'),
    import('@/App'),
    import('./DemoBanner'),
    import('@/lib/theme'),
    import('@/index.css'),
    import('./demo.css'),
  ])
  // Fonts come from a stylesheet <link>, which the fetch stand-in does not touch.
  if (!document.querySelector(`link[href="${APP_FONTS}"]`)) {
    const link = document.createElement('link')
    link.rel = 'stylesheet'
    link.href = APP_FONTS
    document.head.appendChild(link)
  }
  const robots = document.querySelector<HTMLMetaElement>('meta[name="robots"]')
  if (robots) robots.content = 'noindex, follow'
  document.title = 'Nexus demo: sample workspace'
  document.documentElement.classList.remove('nx-home')
  document.documentElement.classList.add('nx-app', 'nx-demo')
  initTheme()
  const container = document.getElementById('root')!
  container.textContent = ''
  createRoot(container).render(
    <StrictMode>
      <App basename="/demo" demo />
      <DemoBanner />
    </StrictMode>,
  )
  dismissBootSplash()
}

void boot()

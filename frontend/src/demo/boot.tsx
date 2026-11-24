/*
 * /demo: the real web app on sample data. main.tsx loads this chunk only for
 * /demo and /demo/*; the mocks go in before any app module runs, and nothing
 * here is imported by the normal app routes.
 */
import { installDemoMocks } from './mock'
import { dismissBootSplash } from '@/lib/boot-splash'
import { disableDiagnostics } from '@/lib/diagnostics'

installDemoMocks()
// The demo never sends problem reports, even from the error screen.
disableDiagnostics()

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
  const robots = document.querySelector<HTMLMetaElement>('meta[name="robots"]')
  if (robots) robots.content = 'noindex, follow'
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

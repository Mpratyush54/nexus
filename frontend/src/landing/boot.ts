import './site.css'
import { initLanding } from './client'

async function boot() {
  const root = document.getElementById('root')
  // Production serves prerendered markup; the dev server does not, so render it once here.
  if (root && !root.querySelector('.nx')) {
    const { renderLanding } = await import('./render-dev')
    renderLanding(root)
  }
  initLanding()
}

void boot()

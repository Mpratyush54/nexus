import { flushSync } from 'react-dom'
import { createRoot } from 'react-dom/client'
import { LandingPage } from './LandingPage'

export function renderLanding(container: HTMLElement) {
  const root = createRoot(container)
  flushSync(() => root.render(<LandingPage />))
}

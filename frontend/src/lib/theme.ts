export type ThemePreference = 'system' | 'light' | 'dark'

const KEY = 'nexus.theme'
const media = () =>
  typeof window !== 'undefined' && window.matchMedia
    ? window.matchMedia('(prefers-color-scheme: light)')
    : null

export function getThemePreference(): ThemePreference {
  try {
    const value = localStorage.getItem(KEY)
    return value === 'light' || value === 'dark' ? value : 'system'
  } catch {
    return 'system'
  }
}

export function resolveTheme(pref: ThemePreference): 'light' | 'dark' {
  if (pref !== 'system') return pref
  return media()?.matches ? 'light' : 'dark'
}

/** Sets <html data-theme> and the browser chrome colour. */
export function applyTheme(pref: ThemePreference = getThemePreference()) {
  const theme = resolveTheme(pref)
  document.documentElement.dataset.theme = theme
  // index.html has one theme-color per colour scheme; set both so the active one follows.
  document
    .querySelectorAll('meta[name="theme-color"]')
    .forEach((m) => m.setAttribute('content', theme === 'light' ? '#f7f6f3' : '#0a0a0a'))
}

export function setThemePreference(pref: ThemePreference) {
  try {
    if (pref === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, pref)
  } catch {
    /* storage blocked: still apply for this visit */
  }
  applyTheme(pref)
}

/** Applies the saved theme now and follows the system while set to "system". */
export function initTheme() {
  applyTheme()
  media()?.addEventListener('change', () => {
    if (getThemePreference() === 'system') applyTheme('system')
  })
}

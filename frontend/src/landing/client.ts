/*
 * The only script the landing page runs. No React: the markup is prerendered.
 * Theme toggle, the annotated-file demo, the sticky nav rule and the film.
 */

const THEME_KEY = 'nx-theme'

function systemDark() {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

function currentTheme(): 'light' | 'dark' {
  const set = document.documentElement.dataset.theme
  if (set === 'light' || set === 'dark') return set
  return systemDark() ? 'dark' : 'light'
}

function applyThemeColor() {
  // Only override the system-based pair once the visitor has picked a theme.
  if (!document.documentElement.dataset.theme) return
  const color = currentTheme() === 'dark' ? '#0e100f' : '#f4f5f2'
  document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]').forEach((m) => (m.content = color))
}

function initTheme() {
  const btn = document.querySelector<HTMLButtonElement>('[data-theme-toggle]')
  if (!btn) return
  const label = () => {
    const next = currentTheme() === 'dark' ? 'light' : 'dark'
    btn.setAttribute('aria-label', `Switch to ${next} theme`)
  }
  label()
  btn.addEventListener('click', () => {
    const next = currentTheme() === 'dark' ? 'light' : 'dark'
    document.documentElement.dataset.theme = next
    try {
      localStorage.setItem(THEME_KEY, next)
    } catch {
      /* storage unavailable: theme still applies for this visit */
    }
    label()
    applyThemeColor()
  })
}

function initWhy() {
  const lines = Array.from(document.querySelectorAll<HTMLButtonElement>('.nx-why-line'))
  const note = document.querySelector<HTMLElement>('.nx-why-note')
  if (!lines.length || !note) return
  const field = (name: string) => note.querySelector<HTMLElement>(`[data-why-field="${name}"]`)
  const select = (btn: HTMLButtonElement) => {
    lines.forEach((l) => {
      l.setAttribute('aria-pressed', String(l === btn))
      l.tabIndex = l === btn ? 0 : -1
    })
    const d = btn.dataset
    const set = (name: string, value?: string) => {
      const el = field(name)
      if (el && value != null) el.textContent = value
    }
    set('line', d.line)
    set('agent-name', d.agentName)
    set('session', d.session)
    set('when', d.when)
    set('why', d.why)
    const agent = field('agent')
    if (agent && d.agent) agent.dataset.agent = d.agent
  }
  lines.forEach((btn, i) => {
    btn.addEventListener('click', () => select(btn))
    btn.addEventListener('keydown', (e) => {
      const step = e.key === 'ArrowDown' ? 1 : e.key === 'ArrowUp' ? -1 : 0
      if (!step) return
      e.preventDefault()
      const next = lines[(i + step + lines.length) % lines.length]
      next.focus()
      select(next)
    })
  })
}

function initNav() {
  const nav = document.querySelector('.nx-nav')
  if (!nav) return
  const onScroll = () => nav.classList.toggle('is-scrolled', window.scrollY > 8)
  onScroll()
  window.addEventListener('scroll', onScroll, { passive: true })
  // Close the mobile menu after choosing a link.
  document.querySelectorAll<HTMLAnchorElement>('.nx-menu nav a').forEach((a) =>
    a.addEventListener('click', () => a.closest('details')?.removeAttribute('open')),
  )
}

function initFilm() {
  const fig = document.querySelector<HTMLElement>('[data-film]')
  const video = fig?.querySelector<HTMLVideoElement>('[data-film-video]')
  const poster = fig?.querySelector<HTMLImageElement>('[data-film-poster]')
  const play = fig?.querySelector<HTMLButtonElement>('[data-film-play]')
  const playLabel = play?.querySelector<HTMLElement>('[data-film-play-label]')
  if (!fig || !video || !poster || !play) return
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  let loaded = false
  let failed = 0
  const sources = Array.from(video.querySelectorAll<HTMLSourceElement>('source'))

  const load = () => {
    if (loaded) return
    loaded = true
    poster.addEventListener('load', () => poster.classList.add('is-ready'), { once: true })
    poster.addEventListener('error', () => poster.remove(), { once: true })
    poster.src = poster.dataset.src ?? ''
    video.poster = poster.dataset.src ?? ''
    sources.forEach((s) => {
      s.addEventListener('error', () => {
        failed += 1
        if (failed >= sources.length) fig.classList.add('is-unavailable')
      })
      s.src = s.dataset.src ?? ''
    })
    video.addEventListener('loadeddata', () => video.classList.add('is-ready'), { once: true })
    video.load()
    if (!reduced) void video.play().catch(() => undefined)
  }

  if ('IntersectionObserver' in window) {
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          io.disconnect()
          load()
        }
      },
      { rootMargin: '300px 0px' },
    )
    io.observe(fig)
  } else {
    load()
  }

  play.addEventListener('click', async () => {
    load()
    // The muted loop is a silent cut; "Play with sound" switches to the full film.
    const full = video.dataset.fullSrc
    if (full && video.getAttribute('src') !== full) {
      sources.forEach((s) => s.remove())
      video.src = full
    }
    video.currentTime = 0
    video.muted = false
    video.loop = false
    video.controls = true
    try {
      await video.play()
      video.classList.add('is-ready')
      play.hidden = true
      video.focus()
    } catch {
      video.muted = true
      if (playLabel) playLabel.textContent = 'Film unavailable right now'
    }
  })
}

export function initLanding() {
  initTheme()
  applyThemeColor()
  initWhy()
  initNav()
  initFilm()
}

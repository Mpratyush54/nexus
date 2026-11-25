/*
 * The only script the landing page runs. No React: the markup is prerendered.
 * Theme toggle, the annotated-file demo, the sticky nav rule, the film loop
 * and the film player.
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

const quiet = () =>
  window.matchMedia('(prefers-reduced-motion: reduce)').matches ||
  (navigator as Navigator & { connection?: { saveData?: boolean } }).connection?.saveData === true

/** The silent cut loops behind the play button while the frame is on screen. */
function initFilmLoop() {
  const loop = document.querySelector<HTMLVideoElement>('[data-film-loop]')
  if (!loop || quiet() || !('IntersectionObserver' in window)) return
  let loaded = false
  loop.addEventListener('playing', () => loop.classList.add('is-playing'))
  new IntersectionObserver(([e]) => {
    if (!e.isIntersecting) return loop.pause()
    if (!loaded) {
      loaded = true
      loop.querySelectorAll('source').forEach((s) => (s.src = s.dataset.src ?? ''))
      loop.load()
    }
    void loop.play().catch(() => undefined)
  }, { threshold: 0.25 }).observe(loop)
}

const clock = (t: number) => {
  t = Math.max(0, Math.floor(t || 0))
  return `${Math.floor(t / 60)}:${String(t % 60).padStart(2, '0')}`
}

/** The full film, with music and a captions track, in a modal dialog with its own controls. */
function initPlayer() {
  const dlg = document.querySelector<HTMLDialogElement>('[data-player]')
  const open = document.querySelector<HTMLButtonElement>('[data-film-open]')
  const stage = dlg?.querySelector<HTMLElement>('[data-player-stage]')
  const v = dlg?.querySelector<HTMLVideoElement>('video')
  if (!dlg || !open || !stage || !v || !dlg.showModal) return
  const el = <T extends HTMLElement>(k: string) => dlg.querySelector<T>(`[data-p="${k}"]`)!
  const play = el<HTMLButtonElement>('play')
  const mute = el<HTMLButtonElement>('mute')
  const fs = el<HTMLButtonElement>('fs')
  const seek = el<HTMLInputElement>('seek')
  const time = el('time')
  const dur = el('dur')
  const loop = document.querySelector<HTMLVideoElement>('[data-film-loop]')
  let opener: HTMLElement | null = null
  let idle = 0

  const toggle = () => (v.paused || v.ended ? void v.play().catch(() => undefined) : v.pause())
  const sync = () => {
    const playing = !v.paused && !v.ended
    stage.dataset.state = playing ? 'playing' : 'paused'
    play.setAttribute('aria-label', playing ? 'Pause' : 'Play')
    stage.classList.toggle('is-muted', v.muted)
    mute.setAttribute('aria-label', v.muted ? 'Unmute' : 'Mute')
    fs.setAttribute('aria-label', document.fullscreenElement ? 'Exit full screen' : 'Full screen')
  }
  const tick = () => {
    const d = v.duration || 0
    const p = d ? v.currentTime / d : 0
    seek.value = String(Math.round(p * 1000))
    seek.style.setProperty('--p', `${p * 100}%`)
    seek.setAttribute('aria-valuetext', `${clock(v.currentTime)} of ${clock(d)}`)
    time.textContent = clock(v.currentTime)
    if (d) dur.textContent = clock(d)
  }
  const wake = () => {
    stage.classList.remove('is-idle')
    clearTimeout(idle)
    idle = window.setTimeout(() => stage.classList.add('is-idle'), 2500)
  }
  const step = (s: number) => (v.currentTime = Math.min(Math.max(0, v.currentTime + s), v.duration || 0))

  ;['play', 'pause', 'ended', 'volumechange'].forEach((t) => v.addEventListener(t, sync))
  ;['timeupdate', 'durationchange', 'seeked'].forEach((t) => v.addEventListener(t, tick))
  document.addEventListener('fullscreenchange', sync)
  play.addEventListener('click', toggle)
  el('toggle').addEventListener('click', toggle)
  v.addEventListener('click', toggle)
  mute.addEventListener('click', () => (v.muted = !v.muted))
  seek.addEventListener('input', () => {
    if (v.duration) v.currentTime = (Number(seek.value) / 1000) * v.duration
    tick()
  })
  fs.addEventListener('click', () => {
    if (document.fullscreenElement) return void document.exitFullscreen()
    if (stage.requestFullscreen) return void stage.requestFullscreen().catch(() => undefined)
    // iOS Safari only lets the video element itself go full screen.
    ;(v as HTMLVideoElement & { webkitEnterFullscreen?: () => void }).webkitEnterFullscreen?.()
  })
  el('close').addEventListener('click', () => dlg.close())
  stage.addEventListener('pointermove', wake)
  dlg.addEventListener('keydown', (e) => {
    wake()
    const t = e.target as HTMLElement
    const onButton = t.tagName === 'BUTTON' && (e.key === ' ' || e.key === 'Enter')
    const onSeek = t === seek && e.key.startsWith('Arrow')
    if (onButton || onSeek || e.ctrlKey || e.metaKey || e.altKey) return
    const k = e.key.toLowerCase()
    if (k === ' ' || k === 'k') toggle()
    else if (k === 'arrowleft' || k === 'j') step(-5)
    else if (k === 'arrowright' || k === 'l') step(5)
    else if (k === 'm') v.muted = !v.muted
    else if (k === 'f') fs.click()
    else if (k === 'home') v.currentTime = 0
    else return
    e.preventDefault()
  })
  // A click on the backdrop (outside the stage) closes, like Esc.
  dlg.addEventListener('click', (e) => {
    if (e.target === dlg) dlg.close()
  })
  dlg.addEventListener('close', () => {
    v.pause()
    if (document.fullscreenElement) void document.exitFullscreen()
    document.documentElement.style.overflow = ''
    if (loop?.classList.contains('is-playing')) void loop.play().catch(() => undefined)
    opener?.focus()
  })

  open.addEventListener('click', () => {
    opener = document.activeElement as HTMLElement | null
    if (!v.src) v.src = v.dataset.src ?? ''
    loop?.pause()
    document.documentElement.style.overflow = 'hidden'
    dlg.showModal()
    play.focus()
    v.muted = false
    if (v.ended) v.currentTime = 0
    sync()
    wake()
    void v.play().catch(() => sync())
  })
}

export function initLanding() {
  initTheme()
  applyThemeColor()
  initWhy()
  initNav()
  initFilmLoop()
  initPlayer()
}

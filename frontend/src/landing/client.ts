import { detectOS, type NavigatorLike } from './os'

/*
 * The only script the landing page runs. No React: the markup is prerendered.
 * The OS-aware downloads, the feature tabs, the sticky nav rule, scroll
 * reveals, the film loop and the film player. The page is dark only, so there is no theme code.
 */

/** Offer the download that fits this visitor (src/landing/os.ts); CSS swaps the buttons. */
function initOS() {
  document.documentElement.dataset.os = detectOS(navigator as Navigator & NavigatorLike)
}

/**
 * The feature tabs: an ARIA tablist with roving focus (arrows, Home, End).
 * All panels share one grid cell (see .nx-js in site.css), so switching never
 * moves the page; without JS every panel simply shows in order.
 */
function initTabs() {
  const tabs = Array.from(document.querySelectorAll<HTMLButtonElement>('.nx-tab'))
  if (!tabs.length) return
  const select = (tab: HTMLButtonElement, focus = false) => {
    tabs.forEach((t) => {
      const on = t === tab
      t.setAttribute('aria-selected', String(on))
      t.tabIndex = on ? 0 : -1
      const panel = document.getElementById(t.getAttribute('aria-controls') ?? '')
      panel?.classList.toggle('is-active', on)
      if (panel) panel.inert = !on
    })
    if (focus) tab.focus()
  }
  tabs.forEach((tab, i) => {
    tab.addEventListener('click', () => select(tab))
    tab.addEventListener('keydown', (e) => {
      const last = tabs.length - 1
      const next =
        e.key === 'ArrowRight' ? (i === last ? 0 : i + 1)
        : e.key === 'ArrowLeft' ? (i === 0 ? last : i - 1)
        : e.key === 'Home' ? 0
        : e.key === 'End' ? last
        : -1
      if (next < 0) return
      e.preventDefault()
      select(tabs[next], true)
    })
  })
  select(tabs.find((t) => t.getAttribute('aria-selected') === 'true') ?? tabs[0])
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

/**
 * Screenshots and their notes rise into place the first time they scroll into
 * view. Only with motion allowed: otherwise (and without JS) nothing is hidden.
 */
function initReveal() {
  if (quiet() || !('IntersectionObserver' in window)) return
  const items = Array.from(document.querySelectorAll<HTMLElement>('.nx-panels, .nx-more-grid > *, .nx-more-list li'))
  if (!items.length) return
  document.documentElement.classList.add('nx-reveal-on')
  items.forEach((el) => {
    el.classList.add('nx-reveal')
    // Stagger the notes in a list so they read top to bottom.
    const li = el.closest('li')
    if (li?.parentElement) el.style.setProperty('--i', String(Array.from(li.parentElement.children).indexOf(li)))
  })
  const io = new IntersectionObserver(
    (entries) =>
      entries.forEach((e) => {
        if (!e.isIntersecting) return
        e.target.classList.add('is-in')
        io.unobserve(e.target)
      }),
    { rootMargin: '0px 0px -8% 0px' },
  )
  items.forEach((el) => io.observe(el))
}

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
  initOS()
  initTabs()
  initNav()
  initReveal()
  initFilmLoop()
  initPlayer()
}

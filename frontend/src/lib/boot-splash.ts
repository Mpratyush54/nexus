/**
 * index.html draws a splash (#nx-boot) for app routes before any script runs.
 * Once React has painted its first frame (often the same splash, as the
 * Suspense fallback), fade the static one out and remove it.
 */
export function dismissBootSplash() {
  const el = document.getElementById('nx-boot')
  if (!el) return
  requestAnimationFrame(() =>
    requestAnimationFrame(() => {
      el.style.opacity = '0'
      window.setTimeout(() => el.remove(), 260)
    }),
  )
}

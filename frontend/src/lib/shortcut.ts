/** "Ctrl K" on Windows and Linux, "⌘K" on Apple devices. */
export function shortcutLabel(): string {
  if (typeof navigator === 'undefined') return 'Ctrl K'
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } }
  const platform = nav.userAgentData?.platform || navigator.platform || navigator.userAgent
  return /mac|iphone|ipad|ipod/i.test(platform) ? '⌘K' : 'Ctrl K'
}

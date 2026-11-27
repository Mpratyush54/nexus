/*
 * Which download to offer first. The prerendered page assumes Windows (the
 * no-JS fallback); client.ts sets <html data-os> from this, and CSS shows the
 * matching buttons and notes (elements carry data-for="win mac mobile other").
 */

export type OS = 'win' | 'mac' | 'mobile' | 'other'

export type NavigatorLike = {
  userAgent: string
  platform?: string
  maxTouchPoints?: number
  userAgentData?: { platform?: string; mobile?: boolean }
}

export function detectOS(nav: NavigatorLike): OS {
  const platform = (nav.userAgentData?.platform || nav.platform || '').toLowerCase()
  const ua = nav.userAgent.toLowerCase()
  if (nav.userAgentData?.mobile || /android|iphone|ipad|ipod|mobile/.test(ua) || platform === 'android' || platform === 'ios') return 'mobile'
  if (platform.includes('mac') || /macintosh|mac os x/.test(ua)) {
    // iPadOS asks for desktop sites and reports a Mac; only a touch screen gives it away.
    return (nav.maxTouchPoints ?? 0) > 1 ? 'mobile' : 'mac'
  }
  if (platform.includes('win') || ua.includes('windows')) return 'win'
  return 'other'
}

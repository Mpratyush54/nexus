/**
 * What a key press in the chat composer does.
 *
 * Desktop: Enter sends, Shift+Enter starts a new line.
 * Phone and tablet (touch, no hover): Enter starts a new line and the Send
 * button sends, like ChatGPT and Claude on a phone.
 * Everywhere: Ctrl/Cmd+Enter sends, and nothing happens while an IME is
 * composing (Enter then picks the candidate, it does not send).
 */
export type KeyLike = {
  key: string
  shiftKey?: boolean
  ctrlKey?: boolean
  metaKey?: boolean
  altKey?: boolean
  isComposing?: boolean
  keyCode?: number
}

export type EnterAction = 'send' | 'newline' | null

export function enterAction(e: KeyLike, touch: boolean): EnterAction {
  if (e.key !== 'Enter') return null
  // Safari reports keyCode 229 for the Enter that confirms an IME candidate.
  if (e.isComposing || e.keyCode === 229) return null
  if (e.ctrlKey || e.metaKey) return 'send'
  if (e.shiftKey || e.altKey) return 'newline'
  return touch ? 'newline' : 'send'
}

/** True on phones and tablets, where the on-screen keyboard's Enter should add a line. */
export function isTouchComposer(): boolean {
  if (typeof window === 'undefined' || !window.matchMedia) return false
  return window.matchMedia('(hover: none) and (pointer: coarse)').matches
}

/** Ctrl on Windows and Linux, ⌘ on Apple devices. */
export function isApple(): boolean {
  if (typeof navigator === 'undefined') return false
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } }
  return /mac|iphone|ipad|ipod/i.test(nav.userAgentData?.platform || navigator.platform || navigator.userAgent)
}

/** A key that types a character, with no shortcut modifier held. */
export function isPrintableKey(e: KeyLike): boolean {
  return e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey
}

/**
 * ↑ / ↓ in the composer walk through your earlier prompts, newest first.
 * Returns the next history index (-1 = back to the empty composer), or null
 * when the key should move the caret as usual.
 */
export function recallStep(
  key: string,
  current: number,
  historyLength: number,
  composerText: string,
  recalled: string | undefined,
): number | null {
  // Only from an empty composer, or one still showing an untouched recalled prompt.
  const untouched = composerText === '' || (recalled !== undefined && composerText === recalled)
  if (!untouched || historyLength === 0) return null
  if (key === 'ArrowUp') return current + 1 < historyLength ? current + 1 : null
  if (key === 'ArrowDown') return current >= 0 ? current - 1 : null
  return null
}

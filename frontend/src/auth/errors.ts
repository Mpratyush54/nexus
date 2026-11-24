import { ApiError } from '@/types/api'

export type AuthContext = 'login' | 'signup' | 'verify' | 'resume' | 'resend' | 'forgot' | 'reset' | 'desktop'

export type FriendlyError = {
  title: string
  detail?: string
  /** Which follow-up the page can offer next to the message. */
  action?: 'forgot' | 'login' | 'resend' | 'restart'
}

/**
 * Turns what the server (internal/server/auth_routes.go, routes.go) or the
 * browser said into plain words. Unknown messages fall through as the
 * server's own text, so nothing is hidden.
 */
export function friendlyAuthError(err: unknown, ctx: AuthContext): FriendlyError {
  if (!(err instanceof ApiError)) {
    // fetch() rejects with a TypeError when the network is down or blocked.
    const offline = typeof navigator !== 'undefined' && navigator.onLine === false
    return {
      title: offline ? 'You are offline' : 'Can’t reach Nexus',
      detail: offline
        ? 'Reconnect to the internet, then try again.'
        : 'Check your connection and try again. If it keeps happening, Nexus may be briefly down.',
    }
  }
  const msg = (err.message || '').toLowerCase()
  const status = err.status

  if (status === 429) {
    if (msg.includes('incorrect code')) {
      return { title: 'Too many wrong codes', detail: 'For your safety that code is locked. Send a new code and try again.', action: 'resend' }
    }
    if (msg.includes('too many codes')) {
      return { title: 'Too many codes requested', detail: 'Wait a minute before asking for another code. The last one still works.' }
    }
    return { title: 'Too many attempts', detail: 'Wait a minute, then try again.' }
  }

  if (status === 401) {
    if (ctx === 'login') {
      return {
        title: 'Email, username or password is wrong',
        detail: 'Check for typos and Caps Lock, or reset your password.',
        action: 'forgot',
      }
    }
    if (ctx === 'verify' || ctx === 'reset' || msg.includes('code')) {
      return { title: 'That code isn’t right', detail: 'Check the latest email from Nexus and enter the 6 digits again.' }
    }
    return { title: 'Your session ended', detail: 'Log in again to continue.', action: 'login' }
  }

  if (status === 409 || msg.includes('already taken')) {
    return {
      title: 'That username or email is already in use',
      detail: 'Log in instead, or choose a different username.',
      action: 'login',
    }
  }

  if (msg.includes('expired')) {
    return { title: 'That code has expired', detail: 'Send a new code; it arrives in a few seconds.', action: 'resend' }
  }
  if (msg.includes('no pending signup') || msg.includes('request a verification code first')) {
    return { title: 'No sign-up in progress for this email', detail: 'Start again with your details.', action: 'restart' }
  }
  if (msg.includes('password does not match')) {
    return {
      title: 'This code was requested with a different password',
      detail: 'Start over to set your password again.',
      action: 'restart',
    }
  }
  if (msg.includes('email delivery is not configured')) {
    return { title: 'Email isn’t set up on this server', detail: 'Ask the person who runs your Nexus server to configure email.' }
  }
  if (msg.includes('could not send verification email') || status === 502) {
    return { title: 'We couldn’t send the email', detail: 'Try again in a minute.' }
  }
  if (msg.includes('authentication not configured') || status === 503) {
    return { title: 'Sign-in is unavailable right now', detail: 'The server is not accepting sign-ins. Try again later.' }
  }
  if (status === 403) {
    // The demo explains itself in its 403s; real 403s are rare here.
    return { title: 'Not allowed', detail: err.message }
  }
  if (status >= 500) {
    return { title: 'Something went wrong on our side', detail: 'Nothing was changed. Try again in a moment.' }
  }
  const text = err.message ? err.message.charAt(0).toUpperCase() + err.message.slice(1) : 'Request failed'
  return { title: text.endsWith('.') ? text : `${text}.` }
}

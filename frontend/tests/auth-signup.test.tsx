import assert from 'node:assert/strict'
import test from 'node:test'
import { friendlyAuthError, SUPPORT_EMAIL } from '../src/auth/errors'
import { afterAuthPath, guestRedirect } from '../src/auth/handoff'
import { ApiError } from '../src/types/api'

test('a signed-in visit to /login or /signup goes Home, not to the desktop page', () => {
  assert.equal(guestRedirect(true, 'idle', null), '/app/dashboard')
  assert.equal(guestRedirect(true, 'idle', '/app/chats'), '/app/chats')
  assert.equal(guestRedirect(true, 'idle', '/cli/auth?x=1'), '/cli/auth?x=1')
  // Only in-app destinations are followed.
  assert.equal(guestRedirect(true, 'idle', 'https://evil.example'), '/app/dashboard')
  assert.equal(guestRedirect(true, 'idle', '/login'), '/app/dashboard')
  // Signed out, or a sign-in on the page is handing off: stay on the form.
  assert.equal(guestRedirect(false, 'idle', '/app/chats'), null)
  assert.equal(guestRedirect(true, 'holding', null), null)
  assert.equal(guestRedirect(true, 'covering', null), null)
  assert.equal(afterAuthPath(null), '/app/dashboard')
})

test('a fetch that never got an answer is not reported as a dead connection', () => {
  // What the browser gives the page for a proxy 502/524 without CORS headers.
  const e = friendlyAuthError(new TypeError('Failed to fetch'), 'signup')
  assert.equal(e.title, 'No answer from Nexus')
  assert.match(e.detail ?? '', /code email may still arrive/)
  assert.ok(e.detail?.includes(SUPPORT_EMAIL))
  assert.doesNotMatch(e.title, /can.t reach/i)
})

test('gateway errors while sending a code say the email service is slow, with a ref', () => {
  for (const status of [502, 504, 524]) {
    const e = friendlyAuthError(new ApiError(status, 'Bad Gateway', undefined, 'REF123'), 'signup')
    assert.equal(e.title, 'We couldn’t send the code email')
    assert.match(e.detail ?? '', /email service is slow/)
    assert.match(e.detail ?? '', /ref REF123/)
    assert.ok(e.detail?.includes(SUPPORT_EMAIL))
    assert.equal(e.action, 'resend')
  }
  // Outside the code-sending requests a 502 is not blamed on email.
  const login = friendlyAuthError(new ApiError(502, 'Bad Gateway', undefined, 'R2'), 'login')
  assert.equal(login.title, 'Nexus is not answering right now')
  assert.match(login.detail ?? '', /ref R2/)
})

test('a server without email says so, with the ref', () => {
  const e = friendlyAuthError(new ApiError(503, 'email delivery is not configured on this server', undefined, 'ABC'), 'signup')
  assert.equal(e.title, 'Email sending isn’t set up')
  assert.match(e.detail ?? '', /ref ABC/)
})

test('the last wrong code ends the sign-up and offers a fresh start', () => {
  const e = friendlyAuthError(new ApiError(429, 'too many incorrect codes — start signup again'), 'verify')
  assert.equal(e.action, 'restart')
  const r = friendlyAuthError(new ApiError(429, 'too many incorrect codes — request a new one'), 'reset')
  assert.equal(r.action, 'resend')
})

test('asking for codes too quickly reads as a short wait', () => {
  const e = friendlyAuthError(new ApiError(429, 'too many codes for this email — try again shortly'), 'resend')
  assert.equal(e.title, 'A code was just sent')
})

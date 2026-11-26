import assert from 'node:assert/strict'
import test from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { thinkingHeader, thinkingLive, thinkingMs, thinkingPreview, thinkingSeconds } from '../src/lib/thinking'
import { chatStatus } from '../src/lib/live-chats'
import { ThinkingBlock } from '../src/components/ThinkingBlock'
import { ReplyStatus } from '../src/pages/chats/ReplyStatus'
import { ChatMessage } from '../src/components/ChatMessage'

test('thinking is live only while the turn runs and before the first words', () => {
  assert.equal(thinkingLive({ state: 'running', thinking: 'Looking at the request' }), true)
  assert.equal(thinkingLive({ state: 'running', thinking: 'Looking', text: 'Sure.' }), false)
  assert.equal(thinkingLive({ state: 'completed', thinking: 'Looking' }), false)
  assert.equal(thinkingLive({ state: 'running', thinking: '  ' }), false)
  assert.equal(thinkingLive({ state: 'running' }), false)
})

test('the header ticks while thinking and settles on the real duration', () => {
  assert.equal(thinkingHeader(true, 400), 'Thinking…')
  assert.equal(thinkingHeader(true, 4200), 'Thinking… 4s')
  assert.equal(thinkingHeader(false, 7000), 'Thought for 7s')
  assert.equal(thinkingHeader(false, 300), 'Thought for 1s')
  assert.equal(thinkingHeader(false, 0), 'Thought process')
  assert.equal(thinkingSeconds(65_000), '1m 05s')
})

test('the duration is the desktop measure, or what this page saw while live', () => {
  const live = { state: 'running', thinking: 'x', thinking_ms: 2000 }
  assert.equal(thinkingMs(live, 1000, 6000), 5000) // this page has seen 5s
  assert.equal(thinkingMs(live, 5500, 6000), 2000) // the desktop knows more
  assert.equal(thinkingMs(live, null, 6000), 2000)
  // Finished: only the desktop's number, never "since this page opened".
  assert.equal(thinkingMs({ state: 'completed', thinking: 'x', thinking_ms: 7000 }, 0, 999_999), 7000)
})

test('the live preview shows the latest words', () => {
  assert.equal(thinkingPreview('  short   thought '), 'short thought')
  const long = Array.from({ length: 100 }, (_, i) => `word${i}`).join(' ')
  const preview = thinkingPreview(long, 60)
  assert.ok(preview.startsWith('…word'))
  assert.ok(preview.endsWith('word99'))
  assert.ok(preview.length <= 60)
})

test('a live thinking block shimmers with a preview; a finished one is collapsed', () => {
  const live = renderToStaticMarkup(createElement(ThinkingBlock, { job: { state: 'running', thinking: 'Looking at the retry loop', thinking_ms: 3000 } }))
  assert.match(live, /chat-thinking is-live/)
  assert.match(live, /Thinking… 3s/)
  assert.match(live, /chat-thinking-preview/)
  assert.match(live, /role="status"/)
  const done = renderToStaticMarkup(createElement(ThinkingBlock, { job: { state: 'completed', thinking: 'Looking at the retry loop', thinking_ms: 7000, text: 'Done.' } }))
  assert.match(done, /Thought for 7s/)
  assert.doesNotMatch(done, /is-live|chat-thinking-preview|<details open/)
  assert.match(done, /Looking at the retry loop/) // the full text is one click away
  assert.equal(renderToStaticMarkup(createElement(ThinkingBlock, { job: { state: 'running' } })), '')
})

test('the status line says Thinking only once: it defers to the live thinking block', () => {
  const loud = renderToStaticMarkup(createElement(ReplyStatus, { verb: 'Thinking…', startedAt: Date.now(), touch: false }))
  assert.match(loud, /Thinking…/)
  const quiet = renderToStaticMarkup(createElement(ReplyStatus, { verb: 'Thinking…', startedAt: Date.now(), touch: false, quiet: true }))
  assert.doesNotMatch(quiet, /Thinking/)
  assert.match(quiet, /is-quiet/)
  assert.match(quiet, /Esc/)
  assert.equal(renderToStaticMarkup(createElement(ReplyStatus, { verb: 'x', startedAt: 0, touch: true, quiet: true })), '')
})

test('a reply shows its thinking above the words', () => {
  const html = renderToStaticMarkup(
    createElement(ChatMessage, {
      turn: { idx: -1, role: 'assistant', text_preview: 'Done.' },
      harness: 'claude',
      thinking: createElement(ThinkingBlock, { job: { state: 'completed', thinking: 'Plan', thinking_ms: 2000, text: 'Done.' } }),
    }),
  )
  assert.ok(html.indexOf('Thought for 2s') < html.indexOf('Done.'))
})

test('chat rows show the desktop sidebar dots', () => {
  const now = Date.parse('2026-11-26T09:00:00Z')
  const recent = new Date(now - 60_000).toISOString()
  assert.deepEqual(chatStatus(undefined, undefined, now, { status: 'running', unread: false }, true), { label: 'Running', tone: 'live', dot: 'running' })
  assert.equal(chatStatus(undefined, undefined, now, { status: 'approval', unread: false }, true)?.dot, 'approval')
  assert.equal(chatStatus(undefined, undefined, now, { status: 'queued', unread: false }, true)?.dot, 'queued')
  assert.equal(chatStatus(undefined, undefined, now, { status: 'done', unread: true }, true)?.dot, 'unread')
  assert.equal(chatStatus(undefined, undefined, now, { status: 'error', unread: true }, true)?.dot, 'error')
  assert.equal(chatStatus(undefined, undefined, now, { status: 'interrupted', unread: true }, true)?.dot, 'stopped')
  // A read reply and a read failure have no dot; the open chat is being read.
  assert.equal(chatStatus(undefined, undefined, now, { status: 'done', unread: false }, true), null)
  assert.equal(chatStatus(undefined, undefined, now, { status: 'done', unread: true }, true, true), null)
  // A request from this browser wins over the server's slower view.
  assert.equal(chatStatus(undefined, { state: 'awaiting_approval', at: now }, now, { status: 'done', unread: true }, true)?.dot, 'approval')
  // With server statuses, a recently moved chat is no longer guessed to be running.
  assert.equal(chatStatus(recent, undefined, now, undefined, true), null)
  assert.equal(chatStatus(recent, undefined, now)?.label, 'Running')
})

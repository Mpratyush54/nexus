import assert from 'node:assert/strict'
import test from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { clearDraft, draftKey, hasDraft, readDraft, writeDraft } from '../src/lib/drafts'
import { enterAction, recallStep } from '../src/lib/composer-keys'
import { editIntoComposer, outbox, shortReason, type Outgoing } from '../src/lib/outbox'
import { groupChats } from '../src/lib/chat-prefs'
import { highlight, langLabel } from '../src/lib/highlight'
import { ChatMessage } from '../src/components/ChatMessage'

/** A stand-in for localStorage that outlives a "reload" (a new read). */
function memoryStore() {
  const map = new Map<string, string>()
  return {
    map,
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => void map.set(k, v),
    removeItem: (k: string) => void map.delete(k),
  }
}

test('a draft is kept per chat and survives switching chats and reloading', () => {
  const store = memoryStore()
  writeDraft('chat-a', 'half a thought about retries', store)
  writeDraft('chat-b', 'something else', store)
  // Switching back (or reloading the page) reads the same text again.
  assert.equal(readDraft('chat-a', store), 'half a thought about retries')
  assert.equal(readDraft('chat-b', store), 'something else')
  assert.equal(readDraft('chat-c', store), '')
  assert.ok(store.map.has(draftKey('chat-a')))
  assert.equal(hasDraft('chat-a', store), true)
})

test('an empty or whitespace draft is forgotten, and a send clears it', () => {
  const store = memoryStore()
  writeDraft('chat-a', 'hello', store)
  writeDraft('chat-a', '   \n ', store)
  assert.equal(store.map.size, 0)
  writeDraft('chat-a', 'hello again', store)
  clearDraft('chat-a', store)
  assert.equal(readDraft('chat-a', store), '')
  assert.equal(hasDraft('chat-a', store), false)
})

test('drafts keep their line breaks and never throw when storage is blocked', () => {
  const store = memoryStore()
  writeDraft('chat-a', 'line one\nline two\n', store)
  assert.equal(readDraft('chat-a', store), 'line one\nline two\n')
  const blocked = {
    getItem: () => { throw new Error('blocked') },
    setItem: () => { throw new Error('blocked') },
    removeItem: () => { throw new Error('blocked') },
  }
  assert.doesNotThrow(() => writeDraft('chat-a', 'x', blocked))
  assert.equal(readDraft('chat-a', blocked), '')
  assert.equal(readDraft('chat-a', null), '')
})

test('on a computer, Enter sends and Shift+Enter makes a new line', () => {
  assert.equal(enterAction({ key: 'Enter' }, false), 'send')
  assert.equal(enterAction({ key: 'Enter', shiftKey: true }, false), 'newline')
  assert.equal(enterAction({ key: 'Enter', ctrlKey: true }, false), 'send')
  assert.equal(enterAction({ key: 'Enter', metaKey: true }, false), 'send')
  assert.equal(enterAction({ key: 'a' }, false), null)
})

test('on a phone, Enter makes a new line and the button (or Ctrl/Cmd+Enter) sends', () => {
  assert.equal(enterAction({ key: 'Enter' }, true), 'newline')
  assert.equal(enterAction({ key: 'Enter', shiftKey: true }, true), 'newline')
  assert.equal(enterAction({ key: 'Enter', metaKey: true }, true), 'send')
})

test('Enter that confirms an IME candidate never sends', () => {
  assert.equal(enterAction({ key: 'Enter', isComposing: true }, false), null)
  assert.equal(enterAction({ key: 'Enter', keyCode: 229 }, false), null)
})

test('↑ in an empty composer walks back through earlier prompts, ↓ comes back', () => {
  assert.equal(recallStep('ArrowUp', -1, 3, '', undefined), 0)
  assert.equal(recallStep('ArrowUp', 0, 3, 'newest', 'newest'), 1)
  assert.equal(recallStep('ArrowUp', 2, 3, 'oldest', 'oldest'), null, 'stops at the oldest')
  assert.equal(recallStep('ArrowDown', 1, 3, 'middle', 'middle'), 0)
  assert.equal(recallStep('ArrowDown', 0, 3, 'newest', 'newest'), -1, 'back to empty')
  assert.equal(recallStep('ArrowUp', -1, 3, 'typed text', undefined), null, 'never replaces typing')
  assert.equal(recallStep('ArrowUp', 0, 3, 'newest, edited', 'newest'), null)
  assert.equal(recallStep('ArrowUp', -1, 0, '', undefined), null)
})

test('a sent message shows at once, before the server answers', () => {
  const sending = outbox(null, { type: 'send', id: 'm1', text: 'Run the tests', at: 1 })
  assert.deepEqual(sending, { id: 'm1', text: 'Run the tests', images: [], state: 'sending', at: 1 })
  // The server accepted it: the live reply takes over.
  assert.equal(outbox(sending, { type: 'sent', id: 'm1' }), null)
})

test('a failed send keeps the message, with its reason, until Retry or Edit', () => {
  let out: Outgoing | null = outbox(null, { type: 'send', id: 'm1', text: 'Run the tests', at: 1 })
  out = outbox(out, { type: 'failed', id: 'm1', error: 'MAYA-LAPTOP didn’t answer.' })
  assert.equal(out?.state, 'failed')
  assert.equal(out?.text, 'Run the tests')
  assert.equal(out?.error, 'MAYA-LAPTOP didn’t answer.')
  // A late answer for another message doesn't touch it.
  assert.equal(outbox(out, { type: 'sent', id: 'other' }), out)
  const retrying = outbox(out, { type: 'retry', id: 'm1' })
  assert.equal(retrying?.state, 'sending')
  assert.equal(retrying?.error, undefined)
  assert.equal(outbox(out, { type: 'discard', id: 'm1' }), null)
})

test('Edit puts a failed message back without losing what was typed since', () => {
  assert.equal(editIntoComposer('Run the tests', ''), 'Run the tests')
  assert.equal(editIntoComposer('Run the tests', 'and lint'), 'Run the tests\n\nand lint')
})

test('a long relay error is shortened, with the rest behind Details', () => {
  assert.deepEqual(shortReason('Offline.'), { short: 'Offline.', more: '' })
  const r = shortReason('MAYA-LAPTOP didn’t answer. Open Nexus on it, then select Retry.')
  assert.equal(r.short, 'MAYA-LAPTOP didn’t answer.')
  assert.equal(r.more, 'Open Nexus on it, then select Retry.')
})

test('a failed message renders "Not sent" with Retry and Edit', () => {
  const html = renderToStaticMarkup(
    createElement(ChatMessage, {
      turn: { idx: -3, role: 'user', text_preview: 'Run the tests' },
      harness: 'claude',
      outgoing: { state: 'failed', error: 'MAYA-LAPTOP didn’t answer.', onRetry: () => {}, onEdit: () => {} },
    }),
  )
  assert.match(html, /Run the tests/)
  assert.match(html, /Not sent · MAYA-LAPTOP didn’t answer\./)
  assert.match(html, />Retry</)
  assert.match(html, />Edit</)
  assert.match(html, /is-failed/)
})

test('a message being sent says so and has no actions yet', () => {
  const html = renderToStaticMarkup(
    createElement(ChatMessage, {
      turn: { idx: -3, role: 'user', text_preview: 'Run the tests' },
      harness: 'claude',
      isNew: true,
      outgoing: { state: 'sending', onRetry: () => {}, onEdit: () => {} },
    }),
  )
  assert.match(html, /Sending…/)
  assert.match(html, /is-new/)
  assert.doesNotMatch(html, /chat-actions/)
})

test('replies get Copy, Retry and Fork from here; your messages get Edit', () => {
  const reply = renderToStaticMarkup(
    createElement(ChatMessage, {
      turn: { idx: 2, role: 'assistant', text_preview: 'All 12 tests pass.' },
      harness: 'claude',
      actions: { always: true, onRetry: () => {}, onFork: () => {} },
    }),
  )
  assert.match(reply, /aria-label="Copy reply"/)
  assert.match(reply, /aria-label="Retry"/)
  assert.match(reply, /aria-label="Fork from here"/)
  assert.match(reply, /chat-actions is-always/)
  const mine = renderToStaticMarkup(
    createElement(ChatMessage, {
      turn: { idx: 1, role: 'user', text_preview: 'Run the tests' },
      harness: 'claude',
      actions: { onEdit: () => {} },
    }),
  )
  assert.match(mine, /aria-label="Edit and send again"/)
  assert.doesNotMatch(mine, /Fork from here/)
})

test('code blocks get a language label, a Copy button and light colour', () => {
  const html = renderToStaticMarkup(
    createElement(ChatMessage, {
      turn: { idx: 2, role: 'assistant', text_preview: 'Here:\n\n```ts\nconst n: number = 42 // answer\n```' },
      harness: 'claude',
    }),
  )
  assert.match(html, /<figure class="chat-code">/)
  assert.match(html, /<figcaption><span>TypeScript<\/span>/)
  assert.match(html, /aria-label="Copy code"/)
  assert.match(html, /<pre tabindex="0"/)
  assert.match(html, /<span class="tok-keyword">const<\/span>/)
  assert.match(html, /<span class="tok-number">42<\/span>/)
  assert.match(html, /<span class="tok-comment">\/\/ answer<\/span>/)
})

test('the highlighter never changes the code, and colours diffs by line', () => {
  const code = 'def f(x):\n    return "a\\"b" # done\n'
  assert.equal(highlight(code, 'python').map((t) => t.text).join(''), code)
  const diff = highlight('@@ -1 +1 @@\n-old\n+new\n same', 'diff')
  assert.deepEqual(diff.map((t) => t.kind), ['meta', 'removed', 'added', 'plain'])
  assert.equal(langLabel(''), 'Code')
  assert.equal(langLabel('tsx'), 'TypeScript')
})

test('pinned chats come first, archived ones are set aside', () => {
  const cards = ['a', 'b', 'c', 'd'].map((session_id) => ({ session_id }))
  const g = groupChats(cards, { pinned: ['c', 'zz'], archived: ['b'], titles: {} })
  assert.deepEqual(g.pinned.map((c) => c.session_id), ['c'])
  assert.deepEqual(g.recent.map((c) => c.session_id), ['a', 'd'])
  assert.deepEqual(g.archived.map((c) => c.session_id), ['b'])
})

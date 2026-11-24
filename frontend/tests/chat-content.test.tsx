import assert from 'node:assert/strict'
import test from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readableChatText, toolViews, diffRows } from '../src/lib/chat-content'
import { ChatMessage } from '../src/components/ChatMessage'
import { splitDesktopEvents } from '../src/lib/desktop-events'

test('live events survive every network boundary, including CRLF separators', () => {
  const wire = ': heartbeat\r\n\r\nevent: progress\r\ndata: {"text":"Hello 🌱"}\r\n\r\ndata:{"text":"done"}\r\n\r\n'
  for (let boundary = 0; boundary <= wire.length; boundary++) {
    const first = splitDesktopEvents('', wire.slice(0, boundary))
    const second = splitDesktopEvents(first.pending, wire.slice(boundary))
    assert.deepEqual([...first.data, ...second.data].map(data => JSON.parse(data)), [{text:'Hello 🌱'}, {text:'done'}])
    assert.equal(second.pending, '')
  }
  assert.throws(() => splitDesktopEvents('', 'x'.repeat(3 * 1024 * 1024 + 1)), /display limit/)
})

test('message envelopes are readable without timestamp wrappers or opaque tool records', () => {
  assert.equal(readableChatText('<timestamp>yesterday</timestamp><user_query>Hello **team**</user_query>'), 'Hello **team**')
  assert.equal(readableChatText('{"tool_calls":[{"name":"exec"}]}'), '')
  assert.equal(readableChatText('{"encrypted_content":"private-blob"}'), '')
  assert.equal(readableChatText('{"text":"Readable answer"}'), 'Readable answer')
})
test('nested tool arguments, replacement fragments and secret fields are normalized', () => {
  const result = toolViews([{ function: { name: 'edit', arguments: JSON.stringify({ path: 'a.ts', old_string: 'old', new_string: 'new', api_key: 'sensitive', message: 'g'.repeat(600) }) } }])[0]
  assert.equal(result.name, 'edit'); assert.equal(result.path, 'a.ts')
  assert.match(result.diff, /-old\n\+new/); assert.ok(!result.input.includes('sensitive'))
  assert.match(result.input, /Opaque payload omitted/)
  const live = toolViews([{ tool: 'shell', command: 'npm test', input: 'test input', output: 'passed' }])[0]
  assert.equal(live.name, 'shell'); assert.equal(live.command, 'npm test'); assert.equal(live.output, 'passed')
})
test('diff file headers are not additions and real hunk indexes are kept', () => {
  const rows = diffRows('--- a/a.ts\n+++ b/a.ts\n@@ -40,2 +50,2 @@\n-old\n+new\n same')
  assert.equal(rows.filter(row => row.kind === 'added').length, 1)
  assert.equal(rows[3].before, 40); assert.equal(rows[4].after, 50)
  assert.equal(rows[5].before, 41); assert.equal(rows[5].after, 51)
  const saved = diffRows('@@ saved replacement @@\n-a\n+b')
  assert.equal(saved[1].before, undefined); assert.equal(saved[2].after, undefined)
})
test('Markdown is rendered safely and browser message IDs stay lossless', () => {
  const markup = renderToStaticMarkup(createElement(ChatMessage, {harness:'Codex',turn:{idx:179000000000000000,cursor_idx:'179000000000000001',role:'assistant',text_preview:'**Done**\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1))'}}))
  assert.match(markup, /<strong>Done<\/strong>/); assert.ok(!markup.includes('<script>'))
  assert.ok(!markup.includes('href="javascript:')); assert.ok(!markup.includes('Invalid Date'))
  assert.match(markup, /turn-179000000000000001-assistant/)
})
test('older servers page chats newest-first and search locally', async () => {
  const { pageTurnsLocally } = await import('../src/lib/chat-paging')
  const all = Array.from({ length: 250 }, (_, idx) => ({ idx, role: idx % 2 ? 'assistant' : 'user', text_preview: idx === 7 || idx === 240 ? 'needle here' : 'text' }))
  const latest = pageTurnsLocally(all, '', '', 100)
  assert.equal(latest.items[0].idx, 150); assert.equal(latest.items.at(-1)?.idx, 249); assert.equal(latest.next_cursor, '150')
  const earlier = pageTurnsLocally(all, '150', '', 100)
  assert.equal(earlier.items[0].idx, 50); assert.equal(earlier.next_cursor, '50')
  const oldest = pageTurnsLocally(all, '50', '', 100)
  assert.equal(oldest.items.length, 50); assert.equal(oldest.next_cursor, undefined)
  const found = pageTurnsLocally(all, '', 'NEEDLE', 1)
  assert.deepEqual(found.items.map((t) => t.idx), [7]); assert.equal(found.next_cursor, '7')
  assert.deepEqual(pageTurnsLocally(all, '7', 'needle', 1).items.map((t) => t.idx), [240])
})

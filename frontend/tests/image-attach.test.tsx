import assert from 'node:assert/strict'
import test from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import {
  IMAGE_PROTOCOL,
  MAX_IMAGES,
  UPDATE_DESKTOP_MESSAGE,
  attachBlocker,
  fileProblem,
  fitWithin,
  pastedImages,
  relayErrorMessage,
} from '../src/lib/image-attach'
import { ChatMessage } from '../src/components/ChatMessage'

const file = (name: string, type: string, size = 1000) => ({ name, type, size }) as File

test('an older desktop or a harness without images blocks the 📎 with a clear reason', () => {
  assert.equal(attachBlocker('claude', undefined), UPDATE_DESKTOP_MESSAGE)
  assert.equal(attachBlocker('claude', 1), UPDATE_DESKTOP_MESSAGE)
  assert.match(attachBlocker('cursor', IMAGE_PROTOCOL), /Claude Code, Codex, or OpenCode/)
  assert.equal(attachBlocker('codex', IMAGE_PROTOCOL), '')
})

test('picked files are checked for type, size and count before reading', () => {
  assert.equal(fileProblem(file('a.png', 'image/png'), 0), '')
  assert.equal(fileProblem(file('photo.jpg', 'image/jpeg', 12 * 1024 * 1024), 0), '', 'big photos are resized, not refused')
  assert.match(fileProblem(file('a.png', 'image/png'), MAX_IMAGES), /at most 4/)
  assert.match(fileProblem(file('notes.pdf', 'application/pdf'), 0), /isn't a PNG, JPEG, GIF, or WebP/)
  assert.match(fileProblem(file('IMG_1.HEIC', ''), 0), /HEIC/)
  assert.match(fileProblem(file('big.gif', 'image/gif', 6 * 1024 * 1024), 0), /GIFs can't be shrunk/)
  assert.match(fileProblem(file('empty.png', 'image/png', 0), 0), /empty/)
})

test('pasting a screenshot attaches it; pasting text does not', () => {
  const shot = file('image.png', 'image/png')
  assert.deepEqual(pastedImages({ files: [shot] }), [shot])
  const fromItems = pastedImages({ files: [], items: [{ kind: 'string', type: 'text/plain', getAsFile: () => null }, { kind: 'file', type: 'image/png', getAsFile: () => shot }] })
  assert.deepEqual(fromItems, [shot])
  assert.deepEqual(pastedImages({ files: [file('a.txt', 'text/plain')], items: [] }), [])
  assert.deepEqual(pastedImages(null), [])
})

test('images are resized to fit 2048 px on their longest side', () => {
  assert.deepEqual(fitWithin(4032, 3024), { width: 2048, height: 1536 })
  assert.deepEqual(fitWithin(800, 600), { width: 800, height: 600 })
})

test('relay errors become something a person can act on', () => {
  assert.equal(relayErrorMessage('Update Nexus on your computer to send images'), UPDATE_DESKTOP_MESSAGE)
  assert.match(relayErrorMessage('invalid JSON body: json: unknown field "attachments"'), /server can’t relay images/)
  assert.equal(relayErrorMessage('desktop is offline'), 'desktop is offline')
})

test('a sent message shows its images, and desktop-only images show as a chip', () => {
  const html = renderToStaticMarkup(createElement(ChatMessage, {
    turn: { idx: -2, role: 'user', text_preview: 'what is wrong here?' },
    harness: 'claude',
    images: [{ url: 'blob:shot', name: 'shot.png' }],
  }))
  assert.match(html, /class="chat-images"/)
  assert.match(html, /src="blob:shot"/)
  assert.match(html, /aria-label="Open shot.png"/)
  const captured = renderToStaticMarkup(createElement(ChatMessage, {
    turn: { idx: 5, role: 'user', text_preview: 'look\n<image name=[Image #1] path="C:/Users/me/shot.png">\n</image>' },
    harness: 'claude',
  }))
  assert.match(captured, /Image #1/)
  assert.match(captured, /kept on the computer it was sent from/)
  assert.doesNotMatch(captured, /<img/)
})

test('a base64 image inside message text is never decoded', () => {
  const inline = 'data:image/png;base64,' + 'A'.repeat(200_000)
  const html = renderToStaticMarkup(createElement(ChatMessage, {
    turn: { idx: 7, role: 'assistant', text_preview: `![screen](${inline})` },
    harness: 'claude',
  }))
  assert.doesNotMatch(html, /data:image/)
  assert.ok(html.length < 10_000)
})

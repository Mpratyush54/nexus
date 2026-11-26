import assert from 'node:assert/strict'
import test from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import {
  applyMentions,
  extractMentions,
  stripMentions,
  findTrigger,
  fuzzyMatch,
  highlights,
  parseCommand,
  rankCommands,
  replaceTrigger,
  segments,
  webCommands,
} from '../src/lib/composer-commands'
import { recallStep } from '../src/lib/composer-keys'
import { chatTitle } from '../src/lib/harness-message'
import { ComposerPopup } from '../src/components/ComposerPopup'

test('"/" opens only at the start of the message, "@" at a word start', () => {
  assert.deepEqual(findTrigger('/', 1), { symbol: '/', start: 0, query: '' })
  assert.deepEqual(findTrigger('/mo', 3), { symbol: '/', start: 0, query: 'mo' })
  assert.deepEqual(findTrigger('look at @rea', 12), { symbol: '@', start: 8, query: 'rea' })
  assert.deepEqual(findTrigger('line\n@x', 7), { symbol: '@', start: 5, query: 'x' })
  assert.deepEqual(findTrigger('see @rea and more', 8), { symbol: '@', start: 4, query: 'rea' })
  assert.equal(findTrigger('hello /model', 12), null)
  assert.equal(findTrigger('/model sonnet', 13), null)
  assert.equal(findTrigger('mail@example.com', 16), null)
  assert.equal(findTrigger('/src/app', 8), null)
  assert.equal(findTrigger('', 0), null)
})

test('a whole-message command parses with its argument', () => {
  assert.deepEqual(parseCommand('/model'), { name: 'model', argument: '' })
  assert.deepEqual(parseCommand('  /Model  gpt-5 '), { name: 'model', argument: 'gpt-5' })
  assert.deepEqual(parseCommand('/frontend:review the diff'), { name: 'frontend:review', argument: 'the diff' })
  assert.equal(parseCommand('model'), null)
  assert.equal(parseCommand('/ model'), null)
  assert.equal(parseCommand('/a$b'), null)
})

test('fuzzy matching finds letters in order and says where', () => {
  assert.deepEqual(fuzzyMatch('cmp', 'compact')?.positions, [0, 2, 3])
  assert.deepEqual(fuzzyMatch('act', 'compact')?.positions, [4, 5, 6])
  assert.equal(fuzzyMatch('ledom', 'model'), null)
  assert.ok(fuzzyMatch('new', 'new')!.score > fuzzyMatch('new', 'renew')!.score)
  assert.ok(fuzzyMatch('new', 'renew')!.score > fuzzyMatch('new', 'n_e_w')!.score)
  assert.deepEqual(segments('compact', [0, 2, 3]), [
    { text: 'c', hit: true },
    { text: 'o', hit: false },
    { text: 'mp', hit: true },
    { text: 'act', hit: false },
  ])
})

test('server results highlight only tight matches', () => {
  assert.deepEqual(highlights('ord', 'Order creation is idempotent'), [0, 1, 2])
  assert.deepEqual(highlights('ord', 'Project brief Key decisions'), [])
  assert.deepEqual(highlights('', 'x'), [])
})

test('commands rank by match, keep menu order when nothing is typed, and depend on the agent', () => {
  assert.deepEqual(webCommands('claude').map((c) => c.name), ['model', 'new', 'clear', 'compact', 'help'])
  assert.ok(!webCommands('codex').some((c) => c.name === 'compact'))
  const all = webCommands('claude')
  assert.deepEqual(rankCommands(all, '').map((r) => r.command.name), all.map((c) => c.name))
  assert.equal(rankCommands(all, 'cpt')[0].command.name, 'compact')
  assert.equal(rankCommands(all, 'zzz').length, 0)
})

test('mentions go along as @paths and context blocks', () => {
  const out = applyMentions('Please refactor', [
    { kind: 'file', value: 'src/app/main.ts', label: 'main.ts' },
    { kind: 'folder', value: 'My Docs/', label: 'My Docs/' },
    { kind: 'chat', value: 'c1', label: 'Fix "login" bug', context: 'Moved the token check into middleware.' },
    { kind: 'memory', value: 'm1', label: 'Deploys', context: 'Deploys go out on Tuesdays.' },
  ])
  assert.equal(
    out,
    'Please refactor\n\n@src/app/main.ts @"My Docs/"\n\n' +
      '<context source="Earlier Nexus chat: Fix \'login\' bug" ref="chat:c1">\nMoved the token check into middleware.\n</context>\n\n' +
      '<context source="Nexus memory: Deploys" ref="memory:m1">\nDeploys go out on Tuesdays.\n</context>',
  )
  // The sent bubble takes them back out as chips that know their source.
  const back = extractMentions(out)
  assert.equal(back.text, 'Please refactor\n\n@src/app/main.ts @"My Docs/"')
  assert.deepEqual(
    back.mentions.map((m) => [m.kind, m.value, m.label]),
    [
      ['chat', 'c1', "Fix 'login' bug"],
      ['memory', 'm1', 'Deploys'],
    ],
  )
  assert.equal(stripMentions('Check\n\n<context source="Nexus memory: X" ref="memory:m">\nhalf a pre'), 'Check')
  assert.ok(!chatTitle(undefined, out, 'claude').includes('context'))
  assert.equal(applyMentions('hi ', []), 'hi ')
  assert.equal(applyMentions('look at @a.ts now', [{ kind: 'file', value: 'a.ts', label: 'a.ts' }]), 'look at @a.ts now')
})

test('a pick replaces the typed trigger', () => {
  assert.deepEqual(replaceTrigger('see @rea and more', { symbol: '@', start: 4, query: 'rea' }, 8), { text: 'see and more', caret: 4 })
  assert.deepEqual(replaceTrigger('@x', { symbol: '@', start: 0, query: 'x' }, 2), { text: '', caret: 0 })
  assert.deepEqual(replaceTrigger('/mo', { symbol: '/', start: 0, query: 'mo' }, 3, '/model '), { text: '/model ', caret: 7 })
})

test('↑ in an empty composer recalls history; typed text is never replaced', () => {
  assert.equal(recallStep('ArrowUp', -1, 2, '', undefined), 0)
  assert.equal(recallStep('ArrowUp', 0, 2, 'last', 'last'), 1)
  assert.equal(recallStep('ArrowUp', 1, 2, 'first', 'first'), null)
  assert.equal(recallStep('ArrowDown', 0, 2, 'last', 'last'), -1)
  assert.equal(recallStep('ArrowUp', -1, 2, 'typed', undefined), null)
})

test('the popup highlights matches and says when nothing matches', () => {
  const html = renderToStaticMarkup(
    createElement(ComposerPopup, {
      id: 'p',
      title: 'Commands',
      rows: [{ id: 'cmd:model', icon: null, title: '/model', titleHits: [1, 2], detail: 'Choose the model', group: 'Commands' }],
      selected: 0,
      empty: 'No commands match.',
      onHover: () => {},
      onPick: () => {},
    }),
  )
  assert.match(html, /role="listbox"/)
  assert.match(html, /aria-selected="true"/)
  assert.match(html, /<mark>mo<\/mark>/)
  const empty = renderToStaticMarkup(
    createElement(ComposerPopup, { id: 'p', title: 'Mention', rows: [], selected: 0, empty: 'No files match', onHover: () => {}, onPick: () => {} }),
  )
  assert.match(empty, /No files match/)
  const loading = renderToStaticMarkup(
    createElement(ComposerPopup, { id: 'p', title: 'Mention', rows: [], selected: 0, empty: 'x', loading: true, onHover: () => {}, onPick: () => {} }),
  )
  assert.match(loading, /Looking…/)
})

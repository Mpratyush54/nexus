import assert from 'node:assert/strict'
import test from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import {
  DictationSession,
  SILENCE_MS,
  dictationErrorText,
  dictationLang,
  dictationShortcutLabel,
  joinPhrases,
  mergeDictation,
  speechRecognitionCtor,
  type Recognition,
  type RecognitionErrorEvent,
  type RecognitionResultEvent,
} from '../src/lib/dictation'
import { VoiceInputButton } from '../src/components/voice/VoiceInputButton'

// ---------- mergeDictation ----------

test('dictating into an empty box starts with a capital', () => {
  assert.deepEqual(mergeDictation('', 'fix the login bug'), { value: 'Fix the login bug', start: 0, end: 17 })
})

test('dictation after typed words joins with one space and keeps the case', () => {
  const m = mergeDictation('Please', 'fix the login bug')
  assert.equal(m.value, 'Please fix the login bug')
  assert.equal(m.value.slice(m.start, m.end), ' fix the login bug')
})

test('a new sentence after a full stop is capitalised; text after the cursor is kept', () => {
  const m = mergeDictation('Done. ', 'now run the tests', 'Thanks')
  assert.equal(m.value, 'Done. Now run the tests Thanks')
  assert.equal(m.value.slice(0, m.start), 'Done. ')
  assert.equal(m.value.slice(m.end), 'Thanks')
  assert.equal(mergeDictation('Is it done?', 'yes').value, 'Is it done? Yes')
  assert.equal(mergeDictation('line one\n', 'two').value, 'line one\nTwo')
})

test('no space goes before punctuation, either side', () => {
  assert.equal(mergeDictation('Hello', ', world').value, 'Hello, world')
  assert.equal(mergeDictation('Say ', 'hi', '.').value, 'Say hi.')
  assert.equal(mergeDictation('Say', 'hi', ' there').value, 'Say hi there')
})

test('empty or blank speech changes nothing', () => {
  assert.deepEqual(mergeDictation('abc', '   ', 'def'), { value: 'abcdef', start: 3, end: 3 })
})

test('recognizer phrases join into one line, whatever spaces they carry', () => {
  assert.equal(joinPhrases(['Fix the bug', ' then test it', '']), 'Fix the bug then test it')
  assert.equal(joinPhrases(['Hello', ', world']), 'Hello, world')
  assert.equal(joinPhrases([]), '')
})

// ---------- support, language, messages ----------

test('speech recognition is found under either name, and missing in Firefox', () => {
  class A {}
  assert.equal(speechRecognitionCtor({ SpeechRecognition: A }), A)
  assert.equal(speechRecognitionCtor({ webkitSpeechRecognition: A }), A)
  assert.equal(speechRecognitionCtor({}), null)
  assert.equal(speechRecognitionCtor(undefined), null)
})

test('the language comes from settings, else the browser', () => {
  assert.equal(dictationLang({ enabled: true, lang: '' }, 'en-IN'), 'en-IN')
  assert.equal(dictationLang({ enabled: true, lang: 'fr-FR' }, 'en-IN'), 'fr-FR')
})

test('errors read calmly and say what to do', () => {
  assert.match(dictationErrorText('no-speech'), /Didn’t catch that/)
  assert.match(dictationErrorText('not-allowed'), /site settings/)
  assert.match(dictationErrorText('audio-capture'), /No microphone/)
})

test('the shortcut reads right on each platform', () => {
  assert.equal(dictationShortcutLabel('MacIntel'), '⌘⇧Space')
  assert.equal(dictationShortcutLabel('Win32'), 'Ctrl+Shift+Space')
})

// ---------- DictationSession with a fake recognizer ----------

class FakeRecognition implements Recognition {
  static all: FakeRecognition[] = []
  lang = ''
  continuous = false
  interimResults = false
  maxAlternatives = 0
  onresult: ((e: RecognitionResultEvent) => void) | null = null
  onerror: ((e: RecognitionErrorEvent) => void) | null = null
  onend: (() => void) | null = null
  onstart: (() => void) | null = null
  started = false
  stopped = false
  aborted = false
  constructor() {
    FakeRecognition.all.push(this)
  }
  start() {
    this.started = true
    this.onstart?.()
  }
  stop() {
    this.stopped = true
    queueMicrotask(() => this.onend?.())
  }
  abort() {
    this.aborted = true
  }
  say(...phrases: Array<[string, boolean]>) {
    const results = phrases.map(([transcript, isFinal]) => ({ isFinal, length: 1, 0: { transcript } }))
    this.onresult?.({ resultIndex: 0, results: Object.assign(results, { length: results.length }) })
  }
}

function fakeTimers() {
  const pending = new Map<number, { fn: () => void; at: number }>()
  let now = 0
  let id = 0
  return {
    set: (fn: () => void, ms: number) => {
      pending.set(++id, { fn, at: now + ms })
      return id
    },
    clear: (t: unknown) => void pending.delete(t as number),
    advance(ms: number) {
      now += ms
      for (const [k, t] of [...pending]) if (t.at <= now) { pending.delete(k); t.fn() }
    },
  }
}

function harness() {
  FakeRecognition.all = []
  const log = { texts: [] as string[], states: [] as string[], notices: [] as string[], done: [] as string[] }
  const timers = fakeTimers()
  const s = new DictationSession(FakeRecognition, 'en-GB', {
    onText: (t) => log.texts.push(t),
    onState: (st) => log.states.push(st),
    onNotice: (n) => log.notices.push(n),
    onDone: (t) => log.done.push(t),
  }, timers)
  return { s, log, timers, rec: () => FakeRecognition.all[FakeRecognition.all.length - 1] }
}

const tick = () => new Promise((r) => setTimeout(r, 0))

test('listening streams interim words, then keeps them on stop (nothing is sent)', async () => {
  const { s, log, rec } = harness()
  s.start()
  assert.equal(rec().lang, 'en-GB')
  assert.equal(rec().continuous, true)
  assert.equal(rec().interimResults, true)
  assert.deepEqual(log.states, ['listening'])
  rec().say(['fix the', false])
  rec().say(['fix the bug.', true], [' then test', false])
  assert.deepEqual(log.texts, ['fix the', 'fix the bug. then test'])
  s.stop()
  await tick()
  assert.deepEqual(log.done, ['fix the bug. then test'])
  assert.deepEqual(log.states, ['listening', 'idle'])
  assert.equal(log.notices.filter(Boolean).length, 0)
})

test('when the browser ends a quiet run, listening restarts and earlier words stay', async () => {
  const { s, log, rec } = harness()
  s.start()
  rec().say(['first part', true])
  const first = rec()
  first.onend?.()
  assert.notEqual(rec(), first, 'a new recognizer run started')
  rec().say(['second part', true])
  assert.equal(log.texts.at(-1), 'first part second part')
  s.stop()
  await tick()
  assert.deepEqual(log.done, ['first part second part'])
})

test('nothing heard: stops after the silence limit and says “Didn’t catch that”', () => {
  const { s, log, timers, rec } = harness()
  s.start()
  timers.advance(SILENCE_MS - 1)
  assert.equal(s.state, 'listening')
  timers.advance(1)
  assert.ok(rec().stopped)
  rec().onend?.()
  assert.equal(s.state, 'idle')
  assert.match(log.notices.at(-1) ?? '', /Didn’t catch that/)
  assert.deepEqual(log.done, [''])
})

test('speaking resets the silence clock', () => {
  const { s, timers, rec } = harness()
  s.start()
  timers.advance(SILENCE_MS - 1000)
  rec().say(['still talking', false])
  timers.advance(SILENCE_MS - 1000)
  assert.equal(s.state, 'listening')
  assert.equal(rec().stopped, false)
})

test('a blocked microphone ends listening with the permission message', () => {
  const { s, log, rec } = harness()
  s.start()
  rec().onerror?.({ error: 'not-allowed' })
  rec().onend?.()
  assert.equal(s.state, 'idle')
  assert.match(log.notices.at(-1) ?? '', /site settings/)
  assert.equal(FakeRecognition.all.length, 1, 'no retry after a permission error')
})

test('an engine that never reports the end still releases the button', () => {
  const { s, log, timers, rec } = harness()
  s.start()
  rec().say(['hello', false])
  rec().stop = () => {}
  s.stop()
  timers.advance(1500)
  assert.equal(s.state, 'idle')
  assert.deepEqual(log.done, ['hello'])
})

// ---------- VoiceInputButton ----------

test('the mic renders with its name, state and shortcut where speech recognition exists', () => {
  const html = renderToStaticMarkup(createElement(VoiceInputButton, { recognition: FakeRecognition }))
  assert.match(html, /aria-label="Voice input"/)
  assert.match(html, /aria-pressed="false"/)
  assert.match(html, /title="Voice input \((Ctrl\+Shift\+Space|⌘⇧Space)\)"/)
  assert.match(html, /aria-live="polite"/)
})

test('no speech recognition (Firefox): no mic at all', () => {
  assert.equal(renderToStaticMarkup(createElement(VoiceInputButton, { recognition: null })), '')
})

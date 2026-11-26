/*
 * Voice input (dictation) for the chat composer, on the browser's own Web
 * Speech API. Nexus never sees or sends audio: the browser's recognizer turns
 * speech into text and only that text lands in the composer.
 *
 * This module holds no React: the text-merge rules, the voice preferences and
 * a small controller around SpeechRecognition, so tests can drive it with a
 * fake recognizer.
 */

// ---------- Text merge ----------

/** The composer text after dictation, and where the dictated part sits in it. */
export type MergedText = { value: string; start: number; end: number }

const SENTENCE_END = /[.!?…]["')\]]*\s*$/
const NO_SPACE_BEFORE = /^[.,!?;:…%)\]}'"’”]/

/**
 * Puts `spoken` between `before` and `after` the way a person would have typed
 * it: one space at each join (none before punctuation), and a capital letter
 * at the start of a sentence. `before` and `after` are kept exactly; only the
 * spoken part and the spaces around it are new, and they sit in
 * value.slice(start, end).
 */
export function mergeDictation(before: string, spoken: string, after = ''): MergedText {
  let piece = spoken.replace(/\s+/g, ' ').trim()
  if (!piece) return { value: before + after, start: before.length, end: before.length }
  const atSentenceStart = before.trim() === '' || SENTENCE_END.test(before) || /\n\s*$/.test(before)
  if (atSentenceStart) piece = piece.charAt(0).toUpperCase() + piece.slice(1)
  if (before && !/\s$/.test(before) && !NO_SPACE_BEFORE.test(piece)) piece = ' ' + piece
  if (after && !/^\s/.test(after) && !NO_SPACE_BEFORE.test(after)) piece = piece + ' '
  return { value: before + piece + after, start: before.length, end: before.length + piece.length }
}

/** Joins recognizer phrases (each may or may not carry its own spaces). */
export function joinPhrases(parts: string[]): string {
  return parts
    .map((p) => p.trim())
    .filter(Boolean)
    .reduce((acc, p) => (acc ? acc + (NO_SPACE_BEFORE.test(p) ? '' : ' ') + p : p), '')
}

// ---------- Support and preferences ----------

export type RecognitionAlternative = { transcript: string; confidence?: number }
export type RecognitionResult = { isFinal: boolean; length: number; [index: number]: RecognitionAlternative }
export type RecognitionResultEvent = { resultIndex: number; results: { length: number; [index: number]: RecognitionResult } }
export type RecognitionErrorEvent = { error: string; message?: string }

/** The part of SpeechRecognition this feature uses. */
export interface Recognition {
  lang: string
  continuous: boolean
  interimResults: boolean
  maxAlternatives: number
  onresult: ((e: RecognitionResultEvent) => void) | null
  onerror: ((e: RecognitionErrorEvent) => void) | null
  onend: (() => void) | null
  onstart: (() => void) | null
  onspeechstart?: (() => void) | null
  start(): void
  stop(): void
  abort(): void
}
export type RecognitionCtor = new () => Recognition

/** SpeechRecognition where the browser has it (Chrome, Edge, Safari); null in Firefox and on the server. */
export function speechRecognitionCtor(scope: unknown = typeof window === 'undefined' ? undefined : window): RecognitionCtor | null {
  if (!scope || typeof scope !== 'object') return null
  const w = scope as { SpeechRecognition?: RecognitionCtor; webkitSpeechRecognition?: RecognitionCtor }
  return w.SpeechRecognition ?? w.webkitSpeechRecognition ?? null
}

export type VoicePrefs = { enabled: boolean; lang: string }
const PREFS_KEY = 'nexus.voice-input'

/** '' means "the browser's language". */
export function readVoicePrefs(): VoicePrefs {
  try {
    const raw = globalThis.localStorage?.getItem(PREFS_KEY)
    if (raw) {
      const p = JSON.parse(raw) as Partial<VoicePrefs>
      return { enabled: p.enabled !== false, lang: typeof p.lang === 'string' ? p.lang : '' }
    }
  } catch {
    // Private windows or blocked storage: defaults.
  }
  return { enabled: true, lang: '' }
}

export function writeVoicePrefs(prefs: VoicePrefs) {
  try {
    globalThis.localStorage?.setItem(PREFS_KEY, JSON.stringify(prefs))
  } catch {
    // Best effort; this tab still uses the choice.
  }
  globalThis.dispatchEvent?.(new Event('nexus-voice-prefs'))
}

export function dictationLang(prefs: VoicePrefs, browserLang = typeof navigator === 'undefined' ? 'en-US' : navigator.language): string {
  return prefs.lang || browserLang || 'en-US'
}

/** Short and calm: shown under the composer, never as a modal. */
export function dictationErrorText(code: string): string {
  switch (code) {
    case 'not-allowed':
    case 'service-not-allowed':
      return 'Microphone is blocked for this site. Allow it in your browser’s site settings (the icon left of the address), then try again.'
    case 'audio-capture':
      return 'No microphone found. Plug one in or check your sound settings.'
    case 'network':
      return 'Voice input in this browser needs an internet connection.'
    case 'language-not-supported':
      return 'This browser can’t recognize that language. Pick another in Settings → Appearance → Voice input.'
    case 'no-speech':
      return 'Didn’t catch that. Try again a little closer to the mic.'
    default:
      return 'Voice input stopped unexpectedly. Try again.'
  }
}

/** "Ctrl+Shift+Space", or "⌘⇧Space" on a Mac. */
export function dictationShortcutLabel(platform = typeof navigator === 'undefined' ? '' : navigator.platform || navigator.userAgent): string {
  return /Mac|iPhone|iPad/i.test(platform) ? '⌘⇧Space' : 'Ctrl+Shift+Space'
}

// ---------- Controller ----------

/** Stop on our own after this long with nothing heard. */
export const SILENCE_MS = 20_000

export type DictationState = 'idle' | 'listening'

export type DictationCallbacks = {
  /** The full text heard so far: finished phrases plus the phrase still being spoken. */
  onText(text: string, final: boolean): void
  onState(state: DictationState): void
  /** A result arrived: drives the level bars. */
  onHeard?(): void
  /** '' clears. */
  onNotice(text: string): void
  /** Ended (by the person, by silence or by an error); `text` is what to keep. */
  onDone(text: string): void
}

type Timers = { set(fn: () => void, ms: number): unknown; clear(id: unknown): void }
const realTimers: Timers = { set: (fn, ms) => setTimeout(fn, ms), clear: (id) => clearTimeout(id as ReturnType<typeof setTimeout>) }

/**
 * One dictation run. Browsers end a continuous recognition on their own after
 * a few seconds of quiet; this restarts it until the person stops, an error
 * ends it, or SILENCE_MS pass with nothing heard.
 */
export class DictationSession {
  state: DictationState = 'idle'
  private rec: Recognition | null = null
  /** Phrases from earlier recognizer runs (after a restart). */
  private kept: string[] = []
  /** This run's text: its final phrases plus the one still being spoken. */
  private current = ''
  private wanted = false
  private running = false
  private fatal = ''
  private silence: unknown = null
  private heardAnything = false
  private readonly Ctor: RecognitionCtor
  private readonly lang: string
  private readonly cb: DictationCallbacks
  private readonly timers: Timers

  constructor(Ctor: RecognitionCtor, lang: string, cb: DictationCallbacks, timers: Timers = realTimers) {
    this.Ctor = Ctor
    this.lang = lang
    this.cb = cb
    this.timers = timers
  }

  start() {
    if (this.running) return
    this.running = true
    this.wanted = true
    this.fatal = ''
    this.kept = []
    this.current = ''
    this.heardAnything = false
    this.cb.onNotice('')
    this.armSilence()
    this.open()
  }

  /** Stop and keep what was heard (the interim phrase included). */
  stop() {
    if (!this.running || !this.wanted) return
    this.wanted = false
    const rec = this.rec
    if (!rec) return this.finish()
    try {
      rec.stop()
    } catch {
      return this.finish()
    }
    // Some engines never fire onend after stop(): don't leave the button stuck.
    this.timers.set(() => {
      if (this.rec === rec) this.finish()
    }, 1500)
  }

  /** The text so far (finished phrases plus the interim one). */
  text(): string {
    return joinPhrases([...this.kept, this.current])
  }

  private open() {
    let rec: Recognition
    try {
      rec = new this.Ctor()
      rec.lang = this.lang
      rec.continuous = true
      rec.interimResults = true
      rec.maxAlternatives = 1
      rec.onstart = () => this.setState('listening')
      rec.onresult = (e) => this.onResult(rec, e)
      rec.onerror = (e) => this.onError(rec, e)
      rec.onend = () => this.onEnd(rec)
      this.rec = rec
      rec.start()
    } catch {
      this.rec = null
      this.fatal = 'unknown'
      this.wanted = false
      this.finish()
      return
    }
    this.setState('listening')
  }

  private onResult(rec: Recognition, e: RecognitionResultEvent) {
    if (rec !== this.rec) return
    const finals: string[] = []
    let interim = ''
    for (let i = 0; i < e.results.length; i++) {
      const r = e.results[i]
      const t = r?.[0]?.transcript ?? ''
      if (r?.isFinal) finals.push(t)
      else interim += t
    }
    // Each run's results start again at 0, so recompute this run in full.
    this.current = joinPhrases([...finals, interim])
    if (this.current) {
      this.heardAnything = true
      this.armSilence()
      this.cb.onHeard?.()
    }
    this.cb.onText(this.text(), interim.trim() === '')
  }

  private onError(rec: Recognition, e: RecognitionErrorEvent) {
    if (rec !== this.rec) return
    // no-speech and aborted: the restart (or the silence timer) handles them.
    if (e.error === 'no-speech' || e.error === 'aborted') return
    this.fatal = e.error || 'unknown'
    this.wanted = false
  }

  private onEnd(rec: Recognition) {
    if (rec !== this.rec) return
    if (this.wanted) {
      // The browser stopped by itself: keep this run's words and listen again.
      if (this.current) this.kept.push(this.current)
      this.current = ''
      this.rec = null
      this.open()
      return
    }
    this.finish()
  }

  private finish() {
    if (!this.running) return
    this.running = false
    this.wanted = false
    const rec = this.rec
    this.rec = null
    if (rec) {
      rec.onresult = rec.onerror = rec.onend = rec.onstart = null
      try {
        rec.abort()
      } catch {
        // already ended
      }
    }
    if (this.current) this.kept.push(this.current)
    this.current = ''
    this.clearSilence()
    const text = this.text()
    if (this.fatal) this.cb.onNotice(dictationErrorText(this.fatal))
    else if (!this.heardAnything || !text) this.cb.onNotice(dictationErrorText('no-speech'))
    this.setState('idle')
    this.cb.onDone(text)
  }

  private armSilence() {
    this.clearSilence()
    this.silence = this.timers.set(() => {
      this.silence = null
      this.stop()
    }, SILENCE_MS)
  }

  private clearSilence() {
    if (this.silence != null) this.timers.clear(this.silence)
    this.silence = null
  }

  private setState(s: DictationState) {
    if (this.state === s) return
    this.state = s
    this.cb.onState(s)
  }
}

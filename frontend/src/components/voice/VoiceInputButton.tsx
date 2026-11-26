import { useCallback, useEffect, useRef, useState } from 'react'
import { Mic, X } from 'lucide-react'
import {
  DictationSession,
  dictationLang,
  dictationShortcutLabel,
  mergeDictation,
  readVoicePrefs,
  speechRecognitionCtor,
  type RecognitionCtor,
  type VoicePrefs,
} from '@/lib/dictation'

type Anchor = { before: string; after: string; written: string; dirty: boolean }

/** Sets a controlled textarea's value so React's onChange sees it (no undo step). */
function writeValue(el: HTMLTextAreaElement, value: string, caret: number) {
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set
  if (setter) setter.call(el, value)
  else el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
  try {
    el.setSelectionRange(caret, caret)
  } catch {
    // detached
  }
}

/**
 * The composer's mic. Click (or Ctrl/⌘+Shift+Space) to dictate; what you say
 * streams into the message box at the cursor. Click again or press Esc to
 * stop: the words stay in the box (nothing is sent), and one Undo removes
 * them. Uses the browser's speech recognition; Nexus never receives audio.
 * Renders nothing where the browser has no speech recognition (Firefox) or
 * voice input is off in Settings.
 *
 * By default it writes to the textarea in the same form.
 */
export function VoiceInputButton({
  target,
  recognition,
}: {
  target?: () => HTMLTextAreaElement | null | undefined
  /** Tests pass a fake recognizer. */
  recognition?: RecognitionCtor | null
}) {
  const [Ctor] = useState<RecognitionCtor | null>(() => recognition ?? speechRecognitionCtor())
  const [prefs, setPrefs] = useState<VoicePrefs>(readVoicePrefs)
  const [listening, setListening] = useState(false)
  const [hearing, setHearing] = useState(false)
  const [notice, setNotice] = useState('')
  const [announce, setAnnounce] = useState('')
  const button = useRef<HTMLButtonElement>(null)
  const session = useRef<DictationSession | null>(null)
  const anchor = useRef<Anchor | null>(null)
  const hearTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const textarea = useCallback((): HTMLTextAreaElement | null => {
    const el = target ? target() : button.current?.closest('form')?.querySelector('textarea')
    return el ?? null
  }, [target])

  useEffect(() => {
    const sync = () => setPrefs(readVoicePrefs())
    window.addEventListener('nexus-voice-prefs', sync)
    window.addEventListener('storage', sync)
    return () => {
      window.removeEventListener('nexus-voice-prefs', sync)
      window.removeEventListener('storage', sync)
    }
  }, [])

  const show = (el: HTMLTextAreaElement, text: string) => {
    const a = anchor.current
    if (!a || a.dirty) return
    if (el.value !== a.written) {
      // Typed into while listening: leave the box alone from here on.
      a.dirty = true
      session.current?.stop()
      return
    }
    const merged = mergeDictation(a.before, text, a.after)
    a.written = merged.value
    writeValue(el, merged.value, merged.end)
  }

  /** Puts the final words in as one edit, so a single Undo takes them out. */
  const commit = (el: HTMLTextAreaElement, text: string) => {
    const a = anchor.current
    anchor.current = null
    if (!a || a.dirty || el.value !== a.written) return
    const merged = mergeDictation(a.before, text, a.after)
    const base = a.before + a.after
    if (el.value !== base) writeValue(el, base, a.before.length)
    const piece = merged.value.slice(merged.start, merged.end)
    if (!piece) return
    el.focus()
    try {
      el.setSelectionRange(a.before.length, a.before.length)
    } catch {
      // detached
    }
    let ok = false
    try {
      ok = typeof document.execCommand === 'function' && document.execCommand('insertText', false, piece)
    } catch {
      ok = false
    }
    if (!ok || el.value !== merged.value) writeValue(el, merged.value, merged.end)
    else el.setSelectionRange(merged.end, merged.end)
  }

  const start = useCallback(() => {
    if (!Ctor || session.current) return
    const el = textarea()
    if (!el || el.disabled || el.readOnly) return
    const from = el.selectionStart ?? el.value.length
    const to = el.selectionEnd ?? from
    anchor.current = { before: el.value.slice(0, from), after: el.value.slice(to), written: el.value, dirty: false }
    const s = new DictationSession(Ctor, dictationLang(readVoicePrefs()), {
      onText: (text) => show(el, text),
      onHeard: () => {
        setHearing(true)
        if (hearTimer.current) clearTimeout(hearTimer.current)
        hearTimer.current = setTimeout(() => setHearing(false), 450)
      },
      onState: (state) => {
        setListening(state === 'listening')
        setAnnounce(state === 'listening' ? 'Listening' : 'Stopped')
      },
      onNotice: setNotice,
      onDone: (text) => {
        session.current = null
        setHearing(false)
        commit(el, text)
      },
    })
    session.current = s
    s.start()
  }, [Ctor, textarea])

  const stop = useCallback(() => session.current?.stop(), [])
  const toggle = useCallback(() => (session.current ? stop() : start()), [start, stop])

  // Ctrl+Shift+Space / ⌘⇧Space toggles from anywhere on the page; Esc stops.
  useEffect(() => {
    if (!Ctor || !prefs.enabled) return
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && (e.code === 'Space' || e.key === ' ')) {
        e.preventDefault()
        toggle()
      } else if (e.key === 'Escape' && session.current) {
        e.preventDefault()
        e.stopPropagation()
        stop()
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [Ctor, prefs.enabled, toggle, stop])

  // Leaving the chat (or turning voice off) never leaves the mic open.
  useEffect(() => {
    if (!prefs.enabled) session.current?.stop()
  }, [prefs.enabled])
  useEffect(
    () => () => {
      session.current?.stop()
      if (hearTimer.current) clearTimeout(hearTimer.current)
    },
    [],
  )

  if (!Ctor || !prefs.enabled) return null
  const shortcut = dictationShortcutLabel()
  const label = listening ? 'Stop voice input' : 'Voice input'
  return (
    <span className="voice-input">
      <button
        ref={button}
        type="button"
        className={'voice-input-button' + (listening ? ' is-listening' : '') + (hearing ? ' is-hearing' : '')}
        aria-label={label}
        aria-pressed={listening}
        aria-keyshortcuts={shortcut === '⌘⇧Space' ? 'Meta+Shift+Space' : 'Control+Shift+Space'}
        title={listening ? `Stop (Esc or ${shortcut})` : `Voice input (${shortcut})`}
        onClick={toggle}
      >
        {listening ? (
          <span className="voice-input-bars" aria-hidden>
            <i />
            <i />
            <i />
            <i />
            <i />
          </span>
        ) : (
          <Mic size={16} aria-hidden />
        )}
      </button>
      <span className="voice-input-live" aria-live="polite" role="status">
        {announce}
      </span>
      {notice ? (
        <span className="voice-input-note" role="alert">
          <span>{notice}</span>
          <button type="button" aria-label="Dismiss" onClick={() => setNotice('')}>
            <X size={12} />
          </button>
        </span>
      ) : null}
    </span>
  )
}

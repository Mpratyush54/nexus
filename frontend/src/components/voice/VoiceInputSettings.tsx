import { useState } from 'react'
import { readVoicePrefs, speechRecognitionCtor, writeVoicePrefs, type VoicePrefs } from '@/lib/dictation'

/** Languages offered besides "Match browser"; the recognizer may know more. */
const VOICE_LANGUAGES: Array<{ value: string; label: string }> = [
  { value: 'en-US', label: 'English (US)' },
  { value: 'en-GB', label: 'English (UK)' },
  { value: 'en-IN', label: 'English (India)' },
  { value: 'hi-IN', label: 'Hindi' },
  { value: 'es-ES', label: 'Spanish' },
  { value: 'fr-FR', label: 'French' },
  { value: 'de-DE', label: 'German' },
  { value: 'pt-BR', label: 'Portuguese (Brazil)' },
  { value: 'it-IT', label: 'Italian' },
  { value: 'ja-JP', label: 'Japanese' },
  { value: 'ko-KR', label: 'Korean' },
  { value: 'zh-CN', label: 'Chinese (Mandarin)' },
]

/** Settings → Appearance → Voice input: on/off and the dictation language, for this browser. */
export function VoiceInputSettings({ labelledBy }: { labelledBy: string }) {
  const [prefs, setPrefs] = useState<VoicePrefs>(readVoicePrefs)
  const supported = speechRecognitionCtor() !== null
  const update = (next: VoicePrefs) => {
    setPrefs(next)
    writeVoicePrefs(next)
  }
  return (
    <div className="flex flex-wrap items-center gap-3" role="group" aria-labelledby={labelledBy}>
      <label className="inline-flex h-9 items-center gap-2 text-sm text-fg">
        <input
          type="checkbox"
          checked={prefs.enabled}
          onChange={(e) => update({ ...prefs, enabled: e.target.checked })}
        />
        Show the mic in chat
      </label>
      <select
        aria-label="Voice input language"
        value={prefs.lang}
        disabled={!prefs.enabled}
        onChange={(e) => update({ ...prefs, lang: e.target.value })}
        className="h-9 rounded-lg border border-border bg-raised px-2 text-sm text-fg"
      >
        <option value="">Match browser ({typeof navigator === 'undefined' ? 'en-US' : navigator.language})</option>
        {VOICE_LANGUAGES.map((l) => (
          <option key={l.value} value={l.value}>
            {l.label}
          </option>
        ))}
      </select>
      {!supported ? (
        <p className="w-full text-xs text-fg-dim">This browser has no speech recognition. Use Chrome, Edge or Safari to dictate.</p>
      ) : null}
    </div>
  )
}

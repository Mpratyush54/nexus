import { useState } from 'react'
import { Monitor, Moon, Sun } from 'lucide-react'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { getThemePreference, setThemePreference, type ThemePreference } from '@/lib/theme'

const OPTIONS: Array<{ value: ThemePreference; label: string; icon: typeof Sun }> = [
  { value: 'system', label: 'Match system', icon: Monitor },
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'dark', label: 'Dark', icon: Moon },
]

export function AppearanceSettings() {
  const [pref, setPref] = useState<ThemePreference>(getThemePreference)
  return (
    <GlassPanel className="space-y-3 p-5">
      <h2 className="text-sm font-medium text-fg" id="appearance-title">
        Appearance
      </h2>
      <div role="radiogroup" aria-labelledby="appearance-title" className="flex flex-wrap gap-2">
        {OPTIONS.map(({ value, label, icon: Icon }) => {
          const on = pref === value
          return (
            <button
              key={value}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => {
                setPref(value)
                setThemePreference(value)
              }}
              className={[
                'inline-flex h-9 items-center gap-2 rounded-lg border px-3 text-sm transition-colors',
                on
                  ? 'border-amber bg-amber-soft text-fg'
                  : 'border-border text-fg-dim hover:border-border-strong hover:text-fg',
              ].join(' ')}
            >
              <Icon size={14} aria-hidden />
              {label}
            </button>
          )
        })}
      </div>
    </GlassPanel>
  )
}

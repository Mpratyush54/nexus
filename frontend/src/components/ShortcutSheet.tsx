import { X } from 'lucide-react'
import { AccessibleModal } from '@/components/ui/AccessibleModal'
import { SHORTCUTS, keyLabel, type Shortcut } from '@/lib/shortcuts'

const GROUPS: Shortcut['group'][] = ['General', 'Chat', 'Composer']

/** Ctrl/Cmd+/: every shortcut, generated from the one list in lib/shortcuts. */
export function ShortcutSheet({ onClose }: { onClose: () => void }) {
  return (
    <AccessibleModal labelledBy="shortcut-sheet-title" onClose={onClose} className="chat-modal nx-shortcuts">
      <header>
        <h3 id="shortcut-sheet-title">Keyboard shortcuts</h3>
        <button type="button" aria-label="Close keyboard shortcuts" onClick={onClose}>
          <X size={18} />
        </button>
      </header>
      {GROUPS.map((group) => (
        <section key={group} aria-labelledby={`shortcuts-${group}`}>
          <h4 id={`shortcuts-${group}`}>{group}</h4>
          <dl>
            {SHORTCUTS.filter((s) => s.group === group).map((s) => (
              <div key={s.label}>
                <dt>{s.label}</dt>
                <dd>
                  {s.keys.map((k) => (
                    <kbd key={k}>{keyLabel(k)}</kbd>
                  ))}
                </dd>
              </div>
            ))}
          </dl>
        </section>
      ))}
      <p className="nx-shortcuts-note">On a phone, Enter adds a new line and the Send button sends.</p>
    </AccessibleModal>
  )
}

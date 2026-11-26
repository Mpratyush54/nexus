import { isApple } from './composer-keys'

/**
 * Every keyboard shortcut in the web app, in one list. The shortcut sheet
 * (Ctrl/Cmd+/) is generated from it, so a new shortcut shows up there by
 * being added here. "Mod" is Ctrl on Windows and Linux, ⌘ on Apple devices.
 */
export type Shortcut = { keys: string[]; label: string; group: 'General' | 'Chat' | 'Composer' }

export const SHORTCUTS: Shortcut[] = [
  { group: 'General', keys: ['Mod', 'K'], label: 'Search chats, memories and actions' },
  { group: 'General', keys: ['Mod', '/'], label: 'Show keyboard shortcuts' },
  { group: 'Chat', keys: ['Mod', 'F'], label: 'Find in this chat' },
  { group: 'Chat', keys: ['Mod', '↓'], label: 'Jump to the latest message' },
  { group: 'Chat', keys: ['Esc'], label: 'Stop the reply' },
  { group: 'Chat', keys: ['Mod', 'Shift', '⌫'], label: 'Stop the reply (from anywhere)' },
  { group: 'Composer', keys: ['Enter'], label: 'Send' },
  { group: 'Composer', keys: ['Shift', 'Enter'], label: 'New line' },
  { group: 'Composer', keys: ['Mod', 'Enter'], label: 'Send (also on a phone keyboard)' },
  { group: 'Composer', keys: ['↑'], label: 'Recall an earlier prompt (empty composer)' },
]

export function keyLabel(key: string, apple = isApple()): string {
  if (key === 'Mod') return apple ? '⌘' : 'Ctrl'
  if (key === 'Shift') return apple ? '⇧' : 'Shift'
  if (key === 'Alt') return apple ? '⌥' : 'Alt'
  return key
}

/** "Ctrl+Shift+⌫" / "⌘⇧⌫" for titles and tooltips. */
export function comboLabel(keys: string[], apple = isApple()): string {
  return keys.map((k) => keyLabel(k, apple)).join(apple ? '' : '+')
}

/** Ctrl on Windows and Linux, ⌘ on Apple devices. */
export function isMod(e: { ctrlKey: boolean; metaKey: boolean }, apple = isApple()): boolean {
  return apple ? e.metaKey : e.ctrlKey
}

/**
 * App-wide commands the palette can run on the page that owns them (the
 * open chat, for example). The page listens; nothing happens if no page does.
 */
export type AppCommand = 'new-chat' | 'find-in-chat' | 'stop-reply' | 'focus-composer' | 'shortcuts'

export function runAppCommand(command: AppCommand) {
  window.dispatchEvent(new CustomEvent<AppCommand>('nexus:command', { detail: command }))
}

export function onAppCommand(handler: (command: AppCommand) => void) {
  const listener = (e: Event) => handler((e as CustomEvent<AppCommand>).detail)
  window.addEventListener('nexus:command', listener)
  return () => window.removeEventListener('nexus:command', listener)
}

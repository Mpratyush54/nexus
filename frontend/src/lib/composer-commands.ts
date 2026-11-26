/**
 * The composer's "/" commands and "@" mentions on the web. Mirrors the
 * desktop rules (desktop/windows/Core/ComposerCommands.cs, FuzzyMatch.cs and
 * ComposerMentions.cs) so both apps behave alike.
 *
 * "/" opens the command list only at the very start of the message; "@"
 * opens mentions at the start or after a space or new line (so an e-mail
 * address never does). The query runs from the symbol to the caret.
 */

export type Trigger = { symbol: '/' | '@'; start: number; query: string }

export function findTrigger(text: string, caret: number): Trigger | null {
  if (caret < 1 || caret > text.length) return null
  let i = caret - 1
  while (i >= 0 && !/\s/.test(text[i]) && text[i] !== '@' && !(text[i] === '/' && i === 0)) i--
  if (i < 0) return null
  if (text[i] === '/' && i === 0) {
    const query = text.slice(1, caret)
    return query.includes('/') ? null : { symbol: '/', start: 0, query }
  }
  if (text[i] === '@' && (i === 0 || /\s/.test(text[i - 1]))) return { symbol: '@', start: i, query: text.slice(i + 1, caret) }
  return null
}

/** "/model sonnet" → { name: "model", argument: "sonnet" }; null unless the whole message is one command. */
export function parseCommand(text: string): { name: string; argument: string } | null {
  const t = text.trim()
  const m = /^\/([\p{L}\p{N}_.:-]+)(?:\s+([\s\S]*))?$/u.exec(t)
  if (!m) return null
  return { name: m[1].toLowerCase(), argument: (m[2] ?? '').trim() }
}

export type FuzzyResult = { score: number; positions: number[] }

const isWordStart = (text: string, i: number) => {
  if (i <= 0) return true
  const prev = text[i - 1]
  const c = text[i]
  if (!/[\p{L}\p{N}]/u.test(prev)) return /[\p{L}\p{N}]/u.test(c)
  return c !== c.toLowerCase() && prev === prev.toLowerCase() && prev !== prev.toUpperCase()
}

/** Letters in order, case-insensitive; a substring (best at a word start) beats a scattered match. */
export function fuzzyMatch(query: string, text: string): FuzzyResult | null {
  const q = query.replace(/\s+/g, '')
  if (!q) return { score: 0, positions: [] }
  if (!text || q.length > text.length) return null
  const lower = text.toLowerCase()
  const ql = q.toLowerCase()
  let at = -1
  for (let from = 0; from <= lower.length - ql.length; ) {
    const found = lower.indexOf(ql, from)
    if (found < 0) break
    if (isWordStart(text, found)) {
      at = found
      break
    }
    from = found + 1
  }
  if (at < 0) at = lower.indexOf(ql)
  if (at >= 0) {
    let score = 1000 + ql.length * 20 - Math.min(at, 200) + (isWordStart(text, at) ? 300 : 0)
    if (at === 0) score += 200
    if (ql.length === text.length) score += 500
    return { score, positions: Array.from({ length: ql.length }, (_, k) => at + k) }
  }
  const positions: number[] = []
  let ti = 0
  for (const c of ql) {
    const found = lower.indexOf(c, ti)
    if (found < 0) return null
    positions.push(found)
    ti = found + 1
  }
  let total = 0
  positions.forEach((p, k) => {
    total += 10
    if (isWordStart(text, p)) total += 30
    if (k > 0 && p === positions[k - 1] + 1) total += 25
    else if (k > 0) total -= Math.min(20, p - positions[k - 1])
  })
  total -= Math.min(100, positions[0])
  return { score: Math.max(1, total), positions }
}

/** Letters to highlight in a result the server found: the match when tight, none when scattered (noise). */
export function highlights(query: string, text: string): number[] {
  const q = query.replace(/\s+/g, '')
  const m = q ? fuzzyMatch(q, text) : null
  if (!m || !m.positions.length) return []
  const span = m.positions[m.positions.length - 1] - m.positions[0] + 1
  return span > q.length * 2 + 2 ? [] : m.positions
}

/** Splits text into runs for display: each piece and whether it matched. */
export function segments(text: string, positions: number[]): { text: string; hit: boolean }[] {
  const hits = new Set(positions)
  const parts: { text: string; hit: boolean }[] = []
  let start = 0
  for (let i = 1; i <= text.length; i++) {
    if (i < text.length && hits.has(i) === hits.has(start)) continue
    parts.push({ text: text.slice(start, i), hit: hits.has(start) })
    start = i
  }
  return parts
}

export type Command = { name: string; description: string; kind: 'action' | 'agent'; takesArgument?: boolean }

/** Agents whose chats can be compacted by sending /compact. */
export const supportsCompact = (agent: string) => agent === 'claude'

/** The commands the web composer offers for this agent, in menu order. */
export function webCommands(agent: string): Command[] {
  const list: Command[] = [
    { name: 'model', description: 'Choose the model for this chat', kind: 'action', takesArgument: true },
    { name: 'new', description: 'Start a new chat', kind: 'action' },
    { name: 'clear', description: 'Start fresh in this project', kind: 'action' },
  ]
  if (supportsCompact(agent)) list.push({ name: 'compact', description: 'Summarize the chat so far to free up context', kind: 'agent' })
  list.push({ name: 'help', description: 'What you can type here', kind: 'action' })
  return list
}

export function rankCommands(commands: Command[], query: string): { command: Command; positions: number[] }[] {
  const q = query.trim()
  const ranked = commands
    .map((command, order) => ({ command, order, m: fuzzyMatch(q, command.name) }))
    .filter((x): x is { command: Command; order: number; m: FuzzyResult } => !!x.m)
  if (q) ranked.sort((a, b) => b.m.score - a.m.score || a.order - b.order)
  return ranked.map((x) => ({ command: x.command, positions: x.m.positions }))
}

export type Mention = {
  kind: 'file' | 'folder' | 'chat' | 'memory'
  /** A relative path (folders end with "/"), or the chat or memory id. */
  value: string
  label: string
  /** For a chat or memory: the text sent along as context. */
  context?: string
}

export const MAX_CONTEXT_CHARS = 6000

export const pathRef = (path: string) => {
  const p = path.replace(/\\/g, '/')
  return p.includes(' ') ? `@"${p}"` : `@${p}`
}

/** The prompt with the mentions: path references after the text, then each chat or memory as a context block. */
export function applyMentions(prompt: string, mentions: Mention[]): string {
  if (!mentions.length) return prompt
  let out = prompt.trimEnd()
  const paths = [...new Set(mentions.filter((m) => m.kind === 'file' || m.kind === 'folder').map((m) => pathRef(m.value)))].filter(
    (p) => !` ${out} `.includes(` ${p} `),
  )
  if (paths.length) out += (out ? '\n\n' : '') + paths.join(' ')
  for (const m of mentions) {
    if (m.kind !== 'chat' && m.kind !== 'memory') continue
    let context = (m.context ?? '').trim()
    if (!context) continue
    if (context.length > MAX_CONTEXT_CHARS) context = context.slice(0, MAX_CONTEXT_CHARS).trimEnd() + '\n…'
    const what = m.kind === 'chat' ? CHAT_SOURCE : MEMORY_SOURCE
    const label = m.label.replace(/"/g, "'").replace(/\n/g, ' ').trim()
    // The id lets the chip under the sent message open its source.
    const ref = m.value ? ` ref="${m.kind}:${m.value.replace(/"/g, "'")}"` : ''
    out += (out ? '\n\n' : '') + `<context source="${what}: ${label}"${ref}>\n${context}\n</context>`
  }
  return out
}

const CHAT_SOURCE = 'Earlier Nexus chat'
const MEMORY_SOURCE = 'Nexus memory'
const CONTEXT_BLOCK = new RegExp(
  `\\s*<context source="(${CHAT_SOURCE}|${MEMORY_SOURCE}): ([^"]*)"(?:\\s+ref="([^"]*)")?\\s*>\\n?([\\s\\S]*?)\\n?</context>`,
  'g',
)
const OPEN_BLOCK = new RegExp(`\\s*<context source="(?:${CHAT_SOURCE}|${MEMORY_SOURCE}):[\\s\\S]*$`)

/**
 * Takes the chats and memories sent with a message back out of it: the text
 * without the context blocks, and each block as a mention for the chips under
 * the sent bubble. Mirrors MentionText.Extract on the desktop.
 */
export function extractMentions(raw: string): { text: string; mentions: Mention[] } {
  if (!raw.includes('<context source="')) return { text: raw, mentions: [] }
  const mentions: Mention[] = []
  const text = raw
    .replace(CONTEXT_BLOCK, (_all, source: string, label: string, ref: string | undefined, body: string) => {
      const kind = source === CHAT_SOURCE ? 'chat' : 'memory'
      const id = ref?.startsWith(kind + ':') ? ref.slice(kind.length + 1) : ''
      mentions.push({ kind, value: id, label: label.trim(), context: body.trim() })
      return ''
    })
    .replace(OPEN_BLOCK, '')
    .trim()
  return { text, mentions }
}

/** The text without chat and memory context blocks (titles and previews). */
export const stripMentions = (raw: string) => extractMentions(raw).text

/** Removes the typed "@query" (or "/query") the pick replaces; returns the new text and caret. */
export function replaceTrigger(text: string, trigger: Trigger, caret: number, insert = ''): { text: string; caret: number } {
  const before = text.slice(0, trigger.start)
  let after = text.slice(Math.max(caret, trigger.start + trigger.query.length + 1))
  if (!insert && before.endsWith(' ') && after.startsWith(' ')) after = after.slice(1)
  return { text: before + insert + after, caret: before.length + insert.length }
}

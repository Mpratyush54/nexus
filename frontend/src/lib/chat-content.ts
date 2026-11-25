import { cleanToolResult, parseTurnText } from './harness-message'

export type ToolView = { name: string; path: string; command: string; input: string; output: string; diff: string; notice: string; subagent: string; brief: string }
/** Hunk header for an edit saved as before/after text, with no line numbers. ChatDiff doesn't show it. */
export const BARE_HUNK = '@@ @@'

const SUBAGENT_TOOL = /^(task|agent)$|sub_?agent|spawn_agent/i
type ObjectValue = Record<string, unknown>
function object(value: unknown): ObjectValue {
  if (typeof value === 'string') { try { return object(JSON.parse(value)) } catch { return {} } }
  return value && typeof value === 'object' && !Array.isArray(value) ? value as ObjectValue : {}
}
function field(value: ObjectValue, ...keys: string[]) { for (const key of keys) if (typeof value[key] === 'string') return value[key] as string; return '' }
function pretty(value: unknown) {
  const text = typeof value === 'string' ? value : value ? JSON.stringify(value, (key, entry) => /^(?:encrypted_content|authorization|api[_-]?key|access[_-]?token|password|secret)$/i.test(key) ? '[redacted]' : typeof entry === 'string' && /^[A-Za-z0-9_+/=-]{400,}$/.test(entry) ? '[Opaque payload omitted]' : entry, 2) : ''
  if (/^[A-Za-z0-9_+/=-]{400,}$/.test(text.trim())) return '[Opaque payload omitted]'
  return text.length > 64000 ? text.slice(0, 64000) + '\n[Display truncated]' : text
}
export function readableChatText(raw: string) {
  let text = raw.trim()
  const envelope = object(text)
  if (Object.keys(envelope).length) {
    const content = field(envelope, 'text', 'text_preview', 'content', 'message')
    if (content) text = content
    else if (envelope.tool_calls || envelope.type === 'tool_call' || envelope.encrypted_content) return ''
  }
  return parseTurnText(text).text.replace(/<\/?(?:user_query|user_message)>/g, '').trim()
}
export function toolViews(raw: unknown): ToolView[] {
  if (typeof raw === 'string') { try { raw = JSON.parse(raw) } catch { return [] } }
  if (!Array.isArray(raw)) return []
  return raw.slice(0, 100).map(value => {
    const call = object(value), fn = object(call.function)
    const args = object(call.args ?? call.input ?? call.arguments ?? fn.arguments)
    const result = object(call.result ?? call.output)
    const success = object(result.success ?? object(result.result).success)
    let diff = field(args, 'diff', 'diffString', 'patch', 'patch_text') || field(success, 'diffString', 'diff') || field(result, 'diffString', 'diff')
    if (!diff && typeof args.old_string === 'string' && typeof args.new_string === 'string' && args.old_string !== args.new_string)
      diff = BARE_HUNK + '\n' + (args.old_string ? args.old_string.split('\n').map(line => '-' + line).join('\n') + '\n' : '') + (args.new_string ? args.new_string.split('\n').map(line => '+' + line).join('\n') : '')
    const name = field(call, 'name', 'tool_name', 'toolName', 'tool') || field(fn, 'name') || 'Tool'
    const rawOutput = pretty(call.result ?? call.output ?? call.error)
    const subagent = SUBAGENT_TOOL.test(name) ? field(args, 'description', 'subagent_type', 'agent', 'name') || 'Sub-agent run' : ''
    return {
      name, subagent,
      brief: subagent ? field(args, 'prompt', 'task', 'instructions') : '',
      path: field(args, 'path', 'file_path', 'filePath', 'target_file', 'relative_workspace_path') || (Array.isArray(call.files) ? String(call.files[0] ?? '') : ''),
      command: field(args, 'command', 'cmd', 'command_line') || field(call, 'command'), input: pretty(Object.keys(args).length ? args : call.input),
      output: subagent ? cleanToolResult(rawOutput) : rawOutput, diff: diff || field(call, 'diff'), notice: field(args, 'notice'),
    }
  })
}
export type DiffRow = { text: string; kind: 'added' | 'removed' | 'context' | 'header'; before?: number; after?: number }
export function diffRows(diff: string): DiffRow[] {
  let before: number | undefined, after: number | undefined, hunk = false
  return diff.replace(/\r\n/g, '\n').split('\n').map(text => {
    if (text.startsWith('@@')) {
      const match = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(text)
      before = match ? Number(match[1]) : undefined; after = match ? Number(match[2]) : undefined; hunk = true
      return { text, kind: 'header' }
    }
    if (text.startsWith('diff --git')) { before = after = undefined; hunk = false; return { text, kind: 'header' } }
    if (!hunk && (text.startsWith('+++ ') || text.startsWith('--- '))) return { text, kind: 'header' }
    if (text.startsWith('+')) { const line = { text, kind: 'added' as const, after }; if (after !== undefined) after++; return line }
    if (text.startsWith('-')) { const line = { text, kind: 'removed' as const, before }; if (before !== undefined) before++; return line }
    if (hunk && text.startsWith(' ')) { const line = { text, kind: 'context' as const, before, after }; if (before !== undefined) before++; if (after !== undefined) after++; return line }
    return { text, kind: 'header' }
  })
}

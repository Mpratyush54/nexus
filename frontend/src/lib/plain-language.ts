import { agentName } from '@/lib/harness-message'
import type { ProjectEvent } from '@/types/api'

function humanize(segment: string): string {
  const words = segment.replace(/[-_.]+/g, ' ').replace(/\s+/g, ' ').trim()
  if (!words) return ''
  return words[0].toUpperCase() + words.slice(1)
}

/** "payments/capture-retries" → { title: "Capture retries", group: "Payments" }. */
export function memoryTitle(key: string): { title: string; group: string } {
  const parts = key.split('/').map((p) => p.trim()).filter(Boolean)
  if (!parts.length) return { title: 'Untitled memory', group: '' }
  const last = parts[parts.length - 1]
  const rest = parts.slice(0, -1).map(humanize).join(' · ')
  return { title: humanize(last) || key, group: rest }
}

/** What an agent's MCP tool call was for, in words. */
const TOOL_PHRASES: Record<string, string> = {
  memory_search: 'searched project memory',
  memory_write: 'saved to project memory',
  memory_propose: 'suggested a memory',
  memory_reflect: 'reviewed project memory',
  project_knowledge: 'read the project brief',
  related_to: 'looked up related memories',
  file_history: "checked a file's history",
  line_history: "checked a line's history",
  why: 'asked why the code changed',
  episode_search: 'searched past chats',
  episode_report: 'saved a chat summary',
  workspace_info: 'checked the workspace',
  file_read: 'read a file',
  file_write: 'wrote a file',
}

export type PlainEvent = {
  /** Agent name when an agent did it, else undefined. */
  agent?: string
  /** Person (user id) behind it. */
  userId?: string
  /** Sentence without the subject, e.g. "saved a memory". */
  action: string
  /** Optional object, e.g. a memory title. */
  object?: string
  kind: 'agent' | 'memory' | 'people'
}

export function describeEvent(ev: ProjectEvent): PlainEvent {
  const p = ev.payload ?? {}
  const str = (v: unknown) => (typeof v === 'string' ? v : '')
  const agentId = ev.agent_id || str(p.agent_id)
  const agent = agentId ? agentName(agentId) : undefined
  const key = str(p.key)
  const object = key ? memoryTitle(key).title : undefined
  switch (ev.event_type) {
    case 'MCP_TOOL_CALL': {
      const tool = str(p.tool_name)
      return { agent: agent ?? 'An agent', userId: ev.user_id, action: TOOL_PHRASES[tool] ?? 'used project memory', kind: 'agent' }
    }
    case 'MEMORY_PROPOSED':
      return { agent, userId: ev.user_id, action: 'saved a memory', object, kind: agent ? 'agent' : 'memory' }
    case 'MEMORY_CONFIRMED':
      return { userId: ev.user_id, action: 'kept a memory', object, kind: 'memory' }
    case 'MEMORY_UPDATED':
      return { agent, userId: ev.user_id, action: 'edited a memory', object, kind: 'memory' }
    case 'MEMORY_REJECTED':
      return { userId: ev.user_id, action: 'forgot a memory', object, kind: 'memory' }
    case 'MEMORY_SUPERSEDED':
      return { userId: ev.user_id, action: 'replaced a memory', object, kind: 'memory' }
    case 'SESSION_HANDOFF_INITIATED':
      return { userId: ev.user_id, action: 'handed off work', object: str(p.note) || undefined, kind: 'people' }
    case 'SESSION_HANDOFF_ACCEPTED':
      return { userId: ev.user_id, action: 'picked up handed-off work', kind: 'people' }
    default:
      return { agent, userId: ev.user_id, action: ev.event_type.replaceAll('_', ' ').toLowerCase(), object, kind: 'people' }
  }
}

/** "Today", "Yesterday", or "Mon, Nov 18". */
export function dayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso)
  const start = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const diff = Math.round((start(now) - start(d)) / 86400000)
  if (diff <= 0) return 'Today'
  if (diff === 1) return 'Yesterday'
  return d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
}

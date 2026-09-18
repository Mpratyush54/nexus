import { motion } from 'framer-motion'
import { useMemo, useState } from 'react'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useActivity } from '@/hooks/useActivity'
import { useAuth } from '@/providers/AuthProvider'
import { formatRelative } from '@/utils/format'
import type { ProjectEvent } from '@/types/api'

const TYPE_OPTIONS = [
  '',
  'MEMORY_PROPOSED',
  'MEMORY_CONFIRMED',
  'MEMORY_UPDATED',
  'MEMORY_REJECTED',
  'MEMORY_SUPERSEDED',
  'MCP_TOOL_CALL',
  'SESSION_HANDOFF_INITIATED',
  'SESSION_HANDOFF_ACCEPTED',
] as const

function eventTone(type: string): 'teal' | 'amber' | 'ember' | 'danger' | 'neutral' {
  if (type.includes('CONFIRMED') || type.includes('ACCEPTED')) return 'teal'
  if (type.includes('PROPOSED') || type.includes('HANDOFF')) return 'amber'
  if (type.includes('REJECTED')) return 'danger'
  if (type.includes('SUPERSEDED') || type.includes('UPDATED')) return 'ember'
  return 'neutral'
}

function payloadSnippet(ev: ProjectEvent) {
  const p = ev.payload ?? {}
  const key = typeof p.key === 'string' ? p.key : ''
  const tool = typeof p.tool_name === 'string' ? p.tool_name : ''
  const agent = typeof p.agent_id === 'string' ? p.agent_id : ''
  if (key) return key
  if (tool && agent) return `${agent} · ${tool}`
  if (tool) return tool
  return ev.event_type.replaceAll('_', ' ').toLowerCase()
}

export function ActivityPage() {
  const { projectId } = useAuth()
  const [type, setType] = useState('')
  const [userId, setUserId] = useState('')
  const [since, setSince] = useState('')
  const feed = useActivity(type, userId.trim(), since)

  const items = feed.data ?? []
  const types = useMemo(() => {
    const extra = new Set<string>(TYPE_OPTIONS.filter(Boolean))
    for (const ev of items) extra.add(ev.event_type)
    return ['', ...[...extra].sort()]
  }, [items])

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Activity</h1>
        <p className="mt-1 text-sm text-fg-dim">
          {projectId
            ? 'Every memory, agent, and session event in this project.'
            : 'Select a project to see the feed.'}
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-2">
        <label className="block">
          <span className="mb-1 block text-xs text-fg-dim">Type</span>
          <select
            value={type}
            onChange={(e) => setType(e.target.value)}
            className="h-10 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
          >
            {types.map((t) => (
              <option key={t || 'all'} value={t}>
                {t ? t : 'All types'}
              </option>
            ))}
          </select>
        </label>
        <label className="block min-w-[12rem] flex-1">
          <span className="mb-1 block text-xs text-fg-dim">User</span>
          <input
            value={userId}
            onChange={(e) => setUserId(e.target.value)}
            placeholder="user id"
            className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-xs text-fg-dim">Since</span>
          <input
            type="date"
            value={since}
            onChange={(e) => setSince(e.target.value)}
            className="h-10 rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
          />
        </label>
      </div>

      {feed.isLoading ? <p className="text-sm text-muted">Loading activity…</p> : null}
      {feed.isError ? <p className="text-sm text-danger">Could not load activity.</p> : null}
      {!feed.isLoading && items.length === 0 ? (
        <p className="text-sm text-fg-dim">No events yet. Create or edit a memory to start the log.</p>
      ) : null}

      <ul className="space-y-2">
        {items.map((ev) => (
          <motion.li
            key={ev.id}
            initial={{ opacity: 0, y: 6 }}
            animate={{ opacity: 1, y: 0 }}
          >
            <GlassPanel className="flex flex-wrap items-baseline gap-x-3 gap-y-1 p-4">
              <StatusPill tone={eventTone(ev.event_type)}>{ev.event_type}</StatusPill>
              <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-fg">
                {payloadSnippet(ev)}
              </span>
              {ev.user_id ? (
                <span className="font-mono text-[11px] text-muted">{ev.user_id}</span>
              ) : null}
              <span className="text-[11px] text-muted">{formatRelative(ev.created_at)}</span>
            </GlassPanel>
          </motion.li>
        ))}
      </ul>
    </div>
  )
}

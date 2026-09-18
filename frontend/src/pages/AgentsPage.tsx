import { motion } from 'framer-motion'
import { Bot, Plus, Trash2 } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { KNOWN_MCP_TOOLS } from '@/api/agents'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import { useAgents, useDeleteAgent, useUpsertAgent } from '@/hooks/useAgents'
import { useAuth } from '@/providers/AuthProvider'
import { ApiError } from '@/types/api'
import { formatRelative } from '@/utils/format'

const MODES = [
  { value: 'full', label: 'Full' },
  { value: 'propose_only', label: 'Propose only' },
  { value: 'read_only', label: 'Read only' },
  { value: 'blocked', label: 'Blocked' },
] as const

function defaultTools(): Record<string, boolean> {
  const tools: Record<string, boolean> = {}
  for (const t of KNOWN_MCP_TOOLS) tools[t] = true
  return tools
}

function modeTone(mode: string): 'teal' | 'amber' | 'ember' | 'neutral' {
  switch (mode) {
    case 'full':
      return 'teal'
    case 'propose_only':
      return 'amber'
    case 'read_only':
      return 'neutral'
    case 'blocked':
      return 'ember'
    default:
      return 'neutral'
  }
}

export function AgentsPage() {
  const { projectId } = useAuth()
  const { push } = useToast()
  const agents = useAgents()
  const upsert = useUpsertAgent()
  const remove = useDeleteAgent()

  const [agentId, setAgentId] = useState('')
  const [mode, setMode] = useState('full')
  const [rate, setRate] = useState(60)
  const [tools, setTools] = useState<Record<string, boolean>>(defaultTools)
  const [editing, setEditing] = useState<string | null>(null)

  const onSave = (e: FormEvent) => {
    e.preventDefault()
    const id = (editing ?? agentId).trim()
    if (!id) return
    upsert.mutate(
      { agentId: id, input: { mode, rate_limit: rate, tools } },
      {
        onSuccess: () => {
          push({ title: editing ? 'Agent updated' : 'Agent added', detail: id, tone: 'teal' })
          setAgentId('')
          setEditing(null)
          setMode('full')
          setRate(60)
          setTools(defaultTools())
        },
        onError: (err) =>
          push({
            title: 'Save failed',
            detail: err instanceof ApiError ? err.message : 'Unknown error',
            tone: 'danger',
          }),
      },
    )
  }

  const startEdit = (id: string) => {
    const a = (agents.data ?? []).find((x) => x.agent_id === id)
    if (!a) return
    setEditing(id)
    setAgentId(id)
    setMode(a.mode || 'full')
    setRate(a.rate_limit || 60)
    setTools({ ...defaultTools(), ...(a.tools ?? {}) })
  }

  if (!projectId) {
    return (
      <div className="py-16 text-sm text-muted">Resolve a project first (sign in again if needed).</div>
    )
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Agents</h1>
        <p className="mt-1 text-sm text-fg-dim">
          MCP permission matrix, rate limits, and connection modes.
        </p>
      </div>

      <GlassPanel className="space-y-4 p-5">
        <h2 className="text-sm font-medium text-fg">
          {editing ? `Edit ${editing}` : 'Add agent'}
        </h2>
        <form onSubmit={onSave} className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-3">
            <label className="block sm:col-span-1">
              <span className="mb-1 block text-xs text-fg-dim">Agent id</span>
              <input
                value={agentId}
                onChange={(e) => setAgentId(e.target.value)}
                disabled={Boolean(editing)}
                required
                placeholder="cursor / claude / custom"
                className="h-10 w-full rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none focus:border-amber disabled:opacity-60"
              />
            </label>
            <label className="block">
              <span className="mb-1 block text-xs text-fg-dim">Mode</span>
              <select
                value={mode}
                onChange={(e) => setMode(e.target.value)}
                className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
              >
                {MODES.map((m) => (
                  <option key={m.value} value={m.value}>
                    {m.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="block">
              <span className="mb-1 block text-xs text-fg-dim">Rate limit / min</span>
              <input
                type="number"
                min={0}
                value={rate}
                onChange={(e) => setRate(Number(e.target.value) || 0)}
                className="h-10 w-full rounded-lg border border-border bg-raised px-3 text-sm text-fg outline-none focus:border-amber"
              />
            </label>
          </div>

          <div>
            <p className="mb-2 text-xs text-fg-dim">Tools</p>
            <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
              {KNOWN_MCP_TOOLS.map((tool) => (
                <label
                  key={tool}
                  className="flex cursor-pointer items-center gap-2 rounded-lg border border-border bg-raised/60 px-3 py-2 text-xs text-fg"
                >
                  <input
                    type="checkbox"
                    checked={Boolean(tools[tool])}
                    onChange={(e) =>
                      setTools((prev) => ({ ...prev, [tool]: e.target.checked }))
                    }
                    className="accent-amber"
                  />
                  <span className="font-mono">{tool}</span>
                </label>
              ))}
            </div>
          </div>

          <div className="flex flex-wrap gap-2">
            <Button type="submit" size="sm" disabled={upsert.isPending}>
              <Plus className="h-3.5 w-3.5" />
              {editing ? 'Save changes' : 'Add agent'}
            </Button>
            {editing ? (
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => {
                  setEditing(null)
                  setAgentId('')
                  setMode('full')
                  setRate(60)
                  setTools(defaultTools())
                }}
              >
                Cancel
              </Button>
            ) : null}
          </div>
        </form>
      </GlassPanel>

      <div className="space-y-3">
        {(agents.data ?? []).map((a, i) => (
          <motion.div
            key={a.agent_id}
            initial={{ opacity: 0, y: 8 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ delay: i * 0.03 }}
          >
            <GlassPanel className="flex flex-wrap items-start gap-4 p-4">
              <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-teal-soft text-teal">
                <Bot className="h-5 w-5" />
              </div>
              <div className="min-w-0 flex-1 space-y-2">
                <div className="flex flex-wrap items-center gap-2">
                  <p className="font-mono text-sm text-fg">{a.agent_id}</p>
                  <StatusPill tone={modeTone(a.mode)}>{a.mode}</StatusPill>
                  <span className="text-xs text-muted">{a.rate_limit}/min</span>
                  {a.updated_at ? (
                    <span className="text-xs text-muted">
                      updated {formatRelative(a.updated_at)}
                    </span>
                  ) : null}
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {Object.entries(a.tools ?? {}).map(([tool, allowed]) => (
                    <span
                      key={tool}
                      className={[
                        'rounded border px-1.5 py-0.5 font-mono text-[10px]',
                        allowed
                          ? 'border-teal/30 bg-teal-soft text-teal'
                          : 'border-border text-muted line-through',
                      ].join(' ')}
                    >
                      {tool}
                    </span>
                  ))}
                </div>
              </div>
              <div className="flex gap-1">
                <Button type="button" size="sm" variant="secondary" onClick={() => startEdit(a.agent_id)}>
                  Edit
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={remove.isPending}
                  onClick={() =>
                    remove.mutate(a.agent_id, {
                      onSuccess: () => push({ title: 'Agent removed', detail: a.agent_id }),
                      onError: (err) =>
                        push({
                          title: 'Remove failed',
                          detail: err instanceof ApiError ? err.message : 'Unknown error',
                          tone: 'danger',
                        }),
                    })
                  }
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            </GlassPanel>
          </motion.div>
        ))}
        {agents.isLoading ? <p className="text-sm text-muted">Loading agents…</p> : null}
        {!agents.isLoading && (agents.data ?? []).length === 0 ? (
          <p className="text-sm text-muted">No agents configured yet.</p>
        ) : null}
      </div>
    </div>
  )
}

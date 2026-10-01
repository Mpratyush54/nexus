import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { Button } from '@/components/ui/Button'
import { apiRequest } from '@/lib/api-client'
import { formatRelative } from '@/utils/format'

type TeleportItem = {
  id: string
  session_id: string
  from_user_id: string
  to_user_id: string
  status: string
  preview: string
  created_at: string
}

type TeleportList = { items: TeleportItem[]; count: number }

export function TeleportPage() {
  const qc = useQueryClient()
  const inbox = useQuery({
    queryKey: ['teleports', 'inbox'],
    queryFn: ({ signal }) => apiRequest<TeleportList>('/v1/teleports/inbox', { signal }),
  })
  const sent = useQuery({
    queryKey: ['teleports', 'sent'],
    queryFn: ({ signal }) => apiRequest<TeleportList>('/v1/teleports/sent', { signal }),
  })
  const revoke = useMutation({
    mutationFn: (id: string) => apiRequest(`/v1/teleports/${id}/revoke`, { method: 'POST' }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['teleports'] })
    },
  })
  const accept = useMutation({
    mutationFn: (id: string) => apiRequest(`/v1/teleports/${id}/accept`, { method: 'POST' }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['teleports'] })
    },
  })

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Teleport</h1>
        <p className="mt-1 text-sm text-fg-dim">
          Sessions sent to you, and sessions you sent. Previews are redacted.
        </p>
      </div>
      <TeleportList
        title="Inbox"
        query={inbox}
        empty="Nothing has been teleported to you."
        action={(item) => (
          <Button size="sm" disabled={accept.isPending} onClick={() => accept.mutate(item.id)}>
            Accept
          </Button>
        )}
      />
      <TeleportList
        title="Sent"
        query={sent}
        empty="You have not teleported a session."
        action={(item) => (
          <Button
            size="sm"
            variant="secondary"
            disabled={revoke.isPending}
            onClick={() => revoke.mutate(item.id)}
          >
            Revoke
          </Button>
        )}
      />
    </div>
  )
}

function TeleportList({
  title,
  query,
  empty,
  action,
}: {
  title: string
  query: { isLoading: boolean; isError: boolean; data?: TeleportList }
  empty: string
  action: (item: TeleportItem) => ReactNode
}) {
  const items = query.data?.items ?? []
  return (
    <section className="space-y-3">
      <h2 className="text-xs font-medium uppercase tracking-wide text-muted">{title}</h2>
      {query.isLoading ? <p className="text-sm text-fg-dim">Loading…</p> : null}
      {query.isError ? <p className="text-sm text-danger">Could not load {title.toLowerCase()}.</p> : null}
      {!query.isLoading && items.length === 0 ? (
        <GlassPanel className="p-4">
          <p className="text-sm text-fg-dim">{empty}</p>
        </GlassPanel>
      ) : null}
      {items.map((item) => (
        <GlassPanel key={item.id} className="flex items-start justify-between gap-4 p-4">
          <div>
            <p className="text-sm text-fg">{item.preview || 'Session preview'}</p>
            <p className="mt-1 text-xs text-fg-dim">
              {item.from_user_id} → {item.to_user_id} · {item.status} · {formatRelative(item.created_at)}
            </p>
          </div>
          {action(item)}
        </GlassPanel>
      ))}
    </section>
  )
}

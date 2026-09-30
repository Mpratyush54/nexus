import { useQuery } from '@tanstack/react-query'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { apiRequest } from '@/lib/api-client'
import { formatRelative } from '@/utils/format'

type TimelineCard = {
  session_id: string
  project_id: string
  harness: string
  native_id: string
  title?: string
  summary?: string
  visibility: string
  last_active_at: string
  turn_count: number
  version_state?: string
}

type TimelineResponse = {
  items: TimelineCard[]
  next_cursor?: string
}

export function TimelinePage() {
  const feed = useQuery({
    queryKey: ['timeline'],
    queryFn: ({ signal }) => apiRequest<TimelineResponse>('/v1/timeline?limit=30', { signal }),
  })

  const items = feed.data?.items ?? []

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-fg">Timeline</h1>
        <p className="mt-1 text-sm text-fg-dim">
          Sessions you own or that were shared with you, across projects.
        </p>
      </div>
      {feed.isLoading ? <p className="text-sm text-fg-dim">Loading timeline…</p> : null}
      {feed.isError ? (
        <p className="text-sm text-danger">The timeline could not be loaded.</p>
      ) : null}
      {!feed.isLoading && items.length === 0 ? (
        <GlassPanel className="p-4">
          <p className="text-sm text-fg-dim">
            No sessions yet. Captured harness sessions show up here after they reach the cloud.
          </p>
        </GlassPanel>
      ) : null}
      <div className="space-y-3">
        {items.map((item) => (
          <GlassPanel key={item.session_id} className="p-4">
            <div className="flex items-baseline justify-between gap-4">
              <h2 className="text-base font-medium text-fg">
                {item.title || item.native_id}
              </h2>
              <span className="text-xs text-fg-dim">{formatRelative(item.last_active_at)}</span>
            </div>
            <p className="mt-1 text-xs text-fg-dim">
              {item.harness} · {item.native_id}
              {item.turn_count ? ` · ${item.turn_count} turns` : ''}
              {item.version_state === 'transcript_only' ? ' · files not saved' : ''}
            </p>
            {item.summary ? <p className="mt-2 text-sm text-fg">{item.summary}</p> : null}
          </GlassPanel>
        ))}
      </div>
    </div>
  )
}

import { FolderGit2 } from 'lucide-react'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useTrustedLocalBridge } from '@/hooks/useTrustedLocalBridge'

/**
 * Compact local-folder presence for Home + Desktop only.
 * No file viewer / loopback URL — that confused people (127.0.0.1:7272 is the
 * Desktop status page on this machine, not a product file browser).
 */
export function LocalWorkspacePanel() {
  const { serverView, trusted, identity } = useTrustedLocalBridge()
  const data = serverView.data
  const online = Boolean(data?.online)

  if (identity === 'mismatch' || identity === 'unsigned') return null
  if (!online && !data?.path) return null
  // Teammate presence without a matching local desktop — skip noisy panel.
  if (!trusted && !online) return null

  const branch = data?.branch ?? data?.workspace?.branch
  const dirty = data?.is_dirty ?? data?.workspace?.is_dirty
  const wsPath = data?.path ?? data?.workspace?.path

  return (
    <GlassPanel className="mb-6 space-y-3 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <FolderGit2 className="h-4 w-4 text-teal" />
        <h2 className="text-sm font-medium text-fg">This machine</h2>
        {online ? (
          <StatusPill tone="teal">desktop online</StatusPill>
        ) : (
          <StatusPill tone="amber">stale</StatusPill>
        )}
        {dirty ? <StatusPill tone="amber">uncommitted changes</StatusPill> : null}
      </div>
      <div className="grid gap-2 text-sm sm:grid-cols-2">
        <div>
          <p className="text-xs text-fg-dim">Branch</p>
          <p className="font-mono text-fg">{branch || '—'}</p>
        </div>
        <div>
          <p className="text-xs text-fg-dim">Folder</p>
          <p className="truncate font-mono text-fg" title={wsPath}>
            {wsPath || '—'}
          </p>
        </div>
      </div>
    </GlassPanel>
  )
}

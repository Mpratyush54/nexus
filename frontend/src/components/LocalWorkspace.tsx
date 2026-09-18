import { useMutation } from '@tanstack/react-query'
import { FolderGit2, FileText } from 'lucide-react'
import { useState } from 'react'
import { daemonApi } from '@/api/daemon'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import { useBridgeReachable, useLocalWorkspaceView } from '@/hooks/useDaemon'

export function DaemonBanner() {
  const view = useLocalWorkspaceView()

  if (view.isLoading) return null
  if (view.data?.online) return null

  return (
    <div className="mb-6 rounded-lg border border-border bg-raised/80 px-4 py-3 text-sm text-fg-dim">
      No active workspace daemon for this project. Cloud features still work — run the workspace
      daemon so it can heartbeat git state to the server (the UI reads it from the API, not a
      hardcoded localhost port).
    </div>
  )
}

export function LocalWorkspacePanel() {
  const { push } = useToast()
  const view = useLocalWorkspaceView()
  const data = view.data
  const online = Boolean(data?.online)
  const proxyUrl = data?.proxy_url
  const bridge = useBridgeReachable(online ? proxyUrl : undefined)
  const bridgeOk = bridge.data === true

  const [path, setPath] = useState('README.md')
  const [fileContent, setFileContent] = useState<string | null>(null)

  const readFile = useMutation({
    mutationFn: (p: string) => {
      if (!proxyUrl) throw new Error('No local bridge advertised by daemon')
      return daemonApi.readFile(proxyUrl, p)
    },
    onSuccess: (res) => setFileContent(res.content),
    onError: (err) =>
      push({
        title: 'File read failed',
        detail: err instanceof Error ? err.message : 'Unknown error',
        tone: 'danger',
      }),
  })

  if (!online && !data?.path) return null

  const branch = data?.branch ?? data?.workspace?.branch
  const commit = data?.commit_sha ?? data?.workspace?.commit_sha
  const dirty = data?.is_dirty ?? data?.workspace?.is_dirty
  const wsPath = data?.path ?? data?.workspace?.path

  return (
    <GlassPanel className="mb-6 space-y-4 p-5">
      <div className="flex flex-wrap items-center gap-2">
        <FolderGit2 className="h-4 w-4 text-teal" />
        <h2 className="text-sm font-medium text-fg">Local workspace</h2>
        {online ? (
          <StatusPill tone="teal">via server channel</StatusPill>
        ) : (
          <StatusPill tone="amber">stale</StatusPill>
        )}
        {dirty ? <StatusPill tone="amber">dirty</StatusPill> : null}
        {proxyUrl ? (
          <StatusPill tone={bridgeOk ? 'teal' : 'neutral'}>
            {bridgeOk ? 'bridge reachable' : 'bridge offline'}
          </StatusPill>
        ) : null}
      </div>

      <div className="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
        <div>
          <p className="text-xs text-fg-dim">Branch</p>
          <p className="font-mono text-fg">{branch || '—'}</p>
        </div>
        <div>
          <p className="text-xs text-fg-dim">Commit</p>
          <p className="font-mono text-fg">{(commit || '—').slice(0, 12)}</p>
        </div>
        <div className="sm:col-span-2">
          <p className="text-xs text-fg-dim">Path</p>
          <p className="truncate font-mono text-fg">{wsPath || '—'}</p>
        </div>
      </div>

      {data?.git_log ? (
        <div>
          <p className="mb-1 text-xs text-fg-dim">Recent log (relayed)</p>
          <pre className="max-h-36 overflow-auto rounded-lg border border-border bg-base/40 p-3 font-mono text-[11px] text-fg-dim">
            {data.git_log}
          </pre>
        </div>
      ) : null}

      {proxyUrl && bridgeOk ? (
        <div className="border-t border-border pt-3">
          <div className="mb-2 flex items-center gap-2 text-xs text-fg-dim">
            <FileText className="h-3.5 w-3.5" />
            File viewer
            <span className="font-mono text-[10px] text-muted">{proxyUrl}</span>
          </div>
          <div className="flex flex-wrap gap-2">
            <input
              value={path}
              onChange={(e) => setPath(e.target.value)}
              className="h-9 min-w-[12rem] flex-1 rounded-lg border border-border bg-raised px-3 font-mono text-sm text-fg outline-none focus:border-amber"
            />
            <Button
              type="button"
              size="sm"
              variant="secondary"
              disabled={readFile.isPending || !path.trim()}
              onClick={() => readFile.mutate(path.trim())}
            >
              Read
            </Button>
          </div>
          {fileContent !== null ? (
            <pre className="mt-3 max-h-64 overflow-auto rounded-lg border border-border bg-base/40 p-3 font-mono text-[11px] leading-relaxed text-fg">
              {fileContent}
            </pre>
          ) : null}
        </div>
      ) : online ? (
        <p className="text-xs text-muted">
          File viewer needs a reachable same-machine bridge URL from the daemon heartbeat. Git
          status above is already live via the server channel.
        </p>
      ) : null}
    </GlassPanel>
  )
}

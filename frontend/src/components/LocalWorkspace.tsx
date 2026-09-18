import { useMutation } from '@tanstack/react-query'
import { FolderGit2, FileText } from 'lucide-react'
import { useState } from 'react'
import { daemonApi } from '@/api/daemon'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { useToast } from '@/components/ui/Toast'
import {
  useDaemonHealth,
  useLocalGitLog,
  useLocalGitStatus,
  useLocalWorkspace,
} from '@/hooks/useDaemon'

export function DaemonBanner() {
  const health = useDaemonHealth()
  const online = health.data === true

  if (health.isLoading) return null
  if (online) return null

  return (
    <div className="mb-6 rounded-lg border border-border bg-raised/80 px-4 py-3 text-sm text-fg-dim">
      Local daemon not detected on{' '}
      <span className="font-mono text-fg">127.0.0.1:7272</span>. Cloud features still work — install
      and run the workspace daemon to unlock git status, file viewer, and local bridge.
    </div>
  )
}

export function LocalWorkspacePanel() {
  const { push } = useToast()
  const health = useDaemonHealth()
  const online = health.data === true
  const workspace = useLocalWorkspace(online)
  const status = useLocalGitStatus(online)
  const log = useLocalGitLog(online)
  const [path, setPath] = useState('README.md')
  const [fileContent, setFileContent] = useState<string | null>(null)

  const readFile = useMutation({
    mutationFn: (p: string) => daemonApi.readFile(p),
    onSuccess: (res) => setFileContent(res.content),
    onError: (err) =>
      push({
        title: 'File read failed',
        detail: err instanceof Error ? err.message : 'Unknown error',
        tone: 'danger',
      }),
  })

  if (!online) return null

  const ws = workspace.data
  const st = status.data

  return (
    <GlassPanel className="mb-6 space-y-4 p-5">
      <div className="flex flex-wrap items-center gap-2">
        <FolderGit2 className="h-4 w-4 text-teal" />
        <h2 className="text-sm font-medium text-fg">Local workspace</h2>
        <StatusPill tone="teal">daemon online</StatusPill>
        {st?.is_dirty || ws?.is_dirty ? <StatusPill tone="amber">dirty</StatusPill> : null}
      </div>

      <div className="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
        <div>
          <p className="text-xs text-fg-dim">Branch</p>
          <p className="font-mono text-fg">{st?.branch || ws?.branch || '—'}</p>
        </div>
        <div>
          <p className="text-xs text-fg-dim">Commit</p>
          <p className="font-mono text-fg">
            {(st?.commit || ws?.commit || '—').slice(0, 12)}
          </p>
        </div>
        <div className="sm:col-span-2">
          <p className="text-xs text-fg-dim">Path</p>
          <p className="truncate font-mono text-fg">{ws?.path || '—'}</p>
        </div>
      </div>

      {st?.porcelain ? (
        <pre className="max-h-28 overflow-auto rounded-lg border border-border bg-base/40 p-3 font-mono text-[11px] text-fg-dim">
          {st.porcelain}
        </pre>
      ) : null}

      {log.data?.log ? (
        <div>
          <p className="mb-1 text-xs text-fg-dim">Recent log</p>
          <pre className="max-h-36 overflow-auto rounded-lg border border-border bg-base/40 p-3 font-mono text-[11px] text-fg-dim">
            {log.data.log}
          </pre>
        </div>
      ) : null}

      <div className="border-t border-border pt-3">
        <div className="mb-2 flex items-center gap-2 text-xs text-fg-dim">
          <FileText className="h-3.5 w-3.5" />
          File viewer
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
    </GlassPanel>
  )
}

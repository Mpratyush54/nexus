import { FolderGit2 } from 'lucide-react'
import { useProjects } from '@/hooks/useProjects'
import { useProjectSummaries } from '@/hooks/useProjectSummaries'
import { projectLabel } from '@/lib/project-label'
import { useAuth } from '@/providers/AuthProvider'

/**
 * Every project with its memory count, so it is obvious which project holds
 * the memories. Picking one switches the active project in place.
 */
export function ProjectPicker() {
  const { projectId, setProjectId } = useAuth()
  const projects = useProjects()
  const summaries = useProjectSummaries()
  const list = projects.data ?? []
  if (projects.isLoading) return <p className="text-xs text-muted">Loading projects…</p>
  if (projects.isError) return <p className="text-xs text-danger">Couldn’t load your projects.</p>
  if (!list.length) return null

  const counts = new Map((summaries.data ?? []).map((row) => [row.project.id, row.metrics.memories]))
  const ordered = summaries.data ? summaries.data.map((row) => row.project) : list

  return (
    <div className="-mx-1 overflow-x-auto px-1 pb-1">
      <ul className="flex w-max gap-2" aria-label="Projects">
        {ordered.map((p) => {
          const active = p.id === projectId
          const n = counts.get(p.id)
          return (
            <li key={p.id}>
              <button
                type="button"
                aria-pressed={active}
                onClick={() => setProjectId(p.id)}
                className={[
                  'flex items-center gap-2 rounded-lg border px-3 py-2 text-sm transition',
                  active
                    ? 'border-amber bg-amber-soft text-fg'
                    : 'border-border bg-raised/50 text-fg-dim hover:border-border-strong hover:text-fg',
                ].join(' ')}
              >
                <FolderGit2 size={13} className={active ? 'text-amber' : 'text-muted'} aria-hidden />
                <span className="max-w-[12rem] truncate">{projectLabel(p)}</span>
                <span className="rounded bg-base/60 px-1.5 font-mono text-[11px] tabular-nums text-muted">
                  {n == null ? '…' : n}
                </span>
              </button>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

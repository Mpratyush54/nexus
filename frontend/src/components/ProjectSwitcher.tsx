import { ChevronDown, FolderGit2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useCurrentProject, useProjects } from '@/hooks/useProjects'
import { useAuth } from '@/providers/AuthProvider'
import { projectLabel, projectSubtitle } from '@/lib/project-label'

/** Global project switcher — every page works on one active project. */
export function ProjectSwitcher({ compact = false }: { compact?: boolean }) {
  const { projectId, setProjectId } = useAuth()
  const projects = useProjects()
  const { current } = useCurrentProject()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)

  const items = useMemo(() => projects.data ?? [], [projects.data])
  const activeLabel = current
    ? projectLabel(current)
    : projectId
      ? `${projectId.slice(0, 8)}…`
      : 'Select project'

  return (
    <div className={compact ? 'relative' : 'relative mb-3'}>
      <button
        type="button"
        className={[
          'flex w-full items-center gap-2 rounded-lg border border-border bg-raised/60 px-2.5 text-left transition hover:border-border-strong',
          compact ? 'h-9 text-xs' : 'h-10 text-sm',
        ].join(' ')}
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-haspopup="listbox"
      >
        <FolderGit2 size={14} className="shrink-0 text-amber" />
        <span className="min-w-0 flex-1 truncate text-fg">{activeLabel}</span>
        <ChevronDown size={14} className="shrink-0 text-muted" />
      </button>

      {open ? (
        <>
          <button
            type="button"
            className="fixed inset-0 z-40 cursor-default"
            aria-label="Close project menu"
            onClick={() => setOpen(false)}
          />
          <ul
            role="listbox"
            className="absolute left-0 right-0 z-50 mt-1 max-h-64 overflow-auto rounded-lg border border-border bg-base py-1 shadow-lg"
          >
            {items.map((p) => {
              const active = p.id === projectId
              return (
                <li key={p.id}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={active}
                    className={[
                      'flex w-full flex-col px-3 py-2 text-left text-sm transition hover:bg-raised',
                      active ? 'bg-raised/80' : '',
                    ].join(' ')}
                    onClick={() => {
                      setProjectId(p.id)
                      setOpen(false)
                      navigate('/app/dashboard')
                    }}
                  >
                    <span className="truncate text-fg">{projectLabel(p)}</span>
                    <span className="truncate font-mono text-[10px] text-muted">
                      {projectSubtitle(p)}
                    </span>
                  </button>
                </li>
              )
            })}
            {!items.length ? (
              <li className="px-3 py-3 text-xs text-muted">
                No projects yet. Open a folder in the Desktop app, or add a project under Team & org.
              </li>
            ) : null}
          </ul>
        </>
      ) : null}
    </div>
  )
}

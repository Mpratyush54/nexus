import { NavLink } from 'react-router-dom'

/** Team (this project's people) and Organization share one nav entry. */
export function TeamTabs() {
  const tab = ({ isActive }: { isActive: boolean }) =>
    [
      'rounded-md px-3 py-1.5 text-sm font-medium transition',
      isActive ? 'bg-raised text-fg shadow-sm' : 'text-fg-dim hover:text-fg',
    ].join(' ')
  return (
    <nav aria-label="Team sections" className="inline-flex rounded-lg border border-border bg-surface/60 p-0.5">
      <NavLink to="/app/team" end className={tab}>
        This project
      </NavLink>
      <NavLink to="/app/org" className={tab}>
        Organization
      </NavLink>
    </nav>
  )
}

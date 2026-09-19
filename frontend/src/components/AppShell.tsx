import { useEffect, useState } from 'react'
import { Outlet, useLocation } from 'react-router-dom'
import { CommandPalette } from './CommandPalette'
import { DaemonBanner, LocalWorkspacePanel } from './LocalWorkspace'
import { MobileNav, SideNav } from './SideNav'
import { useEnsureActiveProject } from '@/hooks/useProjectSummaries'

export function AppShell() {
  const [cmdOpen, setCmdOpen] = useState(false)
  const location = useLocation()
  const onHome = location.pathname.includes('/app/dashboard') || location.pathname === '/app'
  useEnsureActiveProject()

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setCmdOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className="mesh-bg app-shell">
      <SideNav onOpenCommand={() => setCmdOpen(true)} />
      <div className="app-content">
        <MobileNav onOpenCommand={() => setCmdOpen(true)} />
        <main className="app-main">
          <div className="app-main-inner">
            <DaemonBanner />
            {/* Home already shows scanner + project; keep the git bridge panel elsewhere */}
            {!onHome ? <LocalWorkspacePanel /> : null}
            <Outlet />
          </div>
        </main>
        <CommandPalette open={cmdOpen} onClose={() => setCmdOpen(false)} />
      </div>
    </div>
  )
}

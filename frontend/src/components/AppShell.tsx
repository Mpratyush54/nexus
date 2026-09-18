import { useEffect, useState } from 'react'
import { Outlet } from 'react-router-dom'
import { CommandPalette } from './CommandPalette'
import { DaemonBanner, LocalWorkspacePanel } from './LocalWorkspace'
import { MobileNav, SideNav } from './SideNav'

export function AppShell() {
  const [cmdOpen, setCmdOpen] = useState(false)

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
            <LocalWorkspacePanel />
            <Outlet />
          </div>
        </main>
        <CommandPalette open={cmdOpen} onClose={() => setCmdOpen(false)} />
      </div>
    </div>
  )
}

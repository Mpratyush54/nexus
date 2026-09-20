import { useEffect, useState } from 'react'
import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { AccountMismatchBanner } from './AccountMismatchBanner'
import { CommandPalette } from './CommandPalette'
import { DaemonBanner, LocalWorkspacePanel } from './LocalWorkspace'
import { MobileNav, SideNav } from './SideNav'
import { useEnsureActiveProject } from '@/hooks/useProjectSummaries'
import { useTrustedLocalBridge } from '@/hooks/useTrustedLocalBridge'
import { storage } from '@/utils/storage'

export function AppShell() {
  const [cmdOpen, setCmdOpen] = useState(false)
  const location = useLocation()
  const path = location.pathname
  const onHome = path.includes('/app/dashboard') || path === '/app' || path.endsWith('/app/')
  const onDesktop = path.includes('/app/connect')
  const onOnboarding = path.includes('/app/onboarding')
  const bridge = useTrustedLocalBridge()
  const identityLocked =
    bridge.identity === 'mismatch' || bridge.identity === 'unsigned'
  const showLocalChrome = (onHome || onDesktop) && !onOnboarding
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

  const onboardingDone = storage.getOnboardingDone()
  if (!onboardingDone && !onOnboarding) {
    return <Navigate to="/app/onboarding" replace />
  }

  // Account mismatch: only Desktop setup is reachable until re-linked.
  if (identityLocked && !onDesktop && !onOnboarding) {
    return <Navigate to="/app/connect" replace />
  }

  if (onOnboarding && !onboardingDone) {
    return (
      <div className="mesh-bg app-shell is-solo">
        <div className="app-content">
          <main className="app-main" style={{ paddingTop: '2rem' }}>
            <div className="app-main-inner">
              <Outlet />
            </div>
          </main>
        </div>
      </div>
    )
  }

  return (
    <div className="mesh-bg app-shell">
      <SideNav onOpenCommand={() => setCmdOpen(true)} />
      <div className="app-content">
        <MobileNav onOpenCommand={() => setCmdOpen(true)} />
        <main className="app-main">
          <div className="app-main-inner">
            {showLocalChrome ? (
              <>
                <AccountMismatchBanner />
                <DaemonBanner />
                <LocalWorkspacePanel />
              </>
            ) : null}
            <Outlet />
          </div>
        </main>
        {!identityLocked ? (
          <CommandPalette open={cmdOpen} onClose={() => setCmdOpen(false)} />
        ) : null}
      </div>
    </div>
  )
}

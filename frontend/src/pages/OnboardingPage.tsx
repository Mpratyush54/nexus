import { ExternalLink, HardDrive, Monitor } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { DesktopInstallPicker } from '@/components/DesktopInstallPicker'
import { useTrustedLocalBridge } from '@/hooks/useTrustedLocalBridge'
import { useAuth } from '@/providers/AuthProvider'
import { storage } from '@/utils/storage'

const STEPS = [
  {
    id: 'account',
    title: 'Your Nexus account',
    body: 'You are signed into the web portal. Desktop must use this same account — never a different login on the same machine.',
  },
  {
    id: 'install',
    title: 'Install Nexus Desktop',
    body: 'One command installs the tray app, daemon, CLI, and registers login/startup for Windows, macOS, or Linux.',
  },
  {
    id: 'signin',
    title: 'Sign in on Desktop',
    body: 'Open Nexus from the Start Menu / Applications / app launcher (or the tray icon). Choose Sign in and complete the browser flow with the same user as this portal.',
  },
  {
    id: 'harvest',
    title: 'Open a project folder',
    body: 'Point Desktop at your repo. It harvests Cursor / OpenCode chats into Memory for that project. Check status anytime on Desktop setup.',
  },
] as const

export function OnboardingPage() {
  const { user } = useAuth()
  const bridge = useTrustedLocalBridge()
  const navigate = useNavigate()

  const finish = () => {
    storage.setOnboardingDone(true)
    navigate('/app/dashboard', { replace: true })
  }

  return (
    <div className="mx-auto w-full max-w-2xl space-y-8 px-1 py-2 sm:py-6">
      <div>
        <p className="text-xs font-medium uppercase tracking-[0.16em] text-amber">Welcome</p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight text-fg sm:text-3xl">
          Get Nexus ready in a few minutes
        </h1>
        <p className="mt-2 max-w-xl text-sm leading-relaxed text-fg-dim">
          Signed in as <span className="font-mono text-fg">{user?.username ?? 'you'}</span>. Desktop
          and the portal must stay on this account so harvest never leaks across users.
        </p>
      </div>

      <ol className="space-y-4">
        {STEPS.map((s, i) => (
          <li key={s.id}>
            <GlassPanel className="flex gap-4 p-5">
              <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-raised text-sm font-semibold text-amber">
                {i + 1}
              </span>
              <div className="min-w-0 flex-1">
                <h2 className="text-sm font-medium text-fg">{s.title}</h2>
                <p className="mt-1 text-sm leading-relaxed text-fg-dim">{s.body}</p>
                {s.id === 'install' ? (
                  <div className="mt-3 space-y-2">
                    <DesktopInstallPicker description="" title="" />
                    <a
                      className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border px-3 text-xs text-fg-dim hover:text-fg"
                      href="https://github.com/Mpratyush54/nexus#readme"
                      target="_blank"
                      rel="noreferrer"
                    >
                      Docs <ExternalLink size={12} />
                    </a>
                  </div>
                ) : null}
                {s.id === 'signin' && bridge.identity === 'match' ? (
                  <p className="mt-2 text-xs text-teal">Desktop is already linked as you.</p>
                ) : null}
                {s.id === 'signin' && bridge.identity === 'mismatch' ? (
                  <p className="mt-2 text-xs text-danger">
                    Desktop is a different account — finish onboarding, then re-link on Desktop setup.
                  </p>
                ) : null}
              </div>
            </GlassPanel>
          </li>
        ))}
      </ol>

      <GlassPanel className="flex flex-col gap-3 p-4 sm:flex-row sm:flex-wrap sm:items-center sm:justify-between">
        <div className="flex items-center gap-2 text-sm text-fg-dim">
          <Monitor size={16} className="shrink-0 text-amber" />
          <HardDrive size={16} className="shrink-0 text-muted" />
          <span>You can revisit Desktop setup anytime from the sidebar.</span>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button type="button" size="sm" variant="secondary" onClick={finish}>
            Skip for now
          </Button>
          <Button type="button" size="sm" onClick={finish}>
            Continue to Home
          </Button>
        </div>
      </GlassPanel>
    </div>
  )
}

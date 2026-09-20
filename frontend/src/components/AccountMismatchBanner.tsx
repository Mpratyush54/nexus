import { AlertTriangle, ExternalLink } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { useTrustedLocalBridge } from '@/hooks/useTrustedLocalBridge'
import { daemonApi } from '@/api/daemon'
import { useToast } from '@/components/ui/Toast'

/**
 * Shown when Nexus Desktop on this machine is signed in as a different user
 * than the web portal — harvest / local bridge stay locked until re-linked.
 */
export function AccountMismatchBanner() {
  const bridge = useTrustedLocalBridge()
  const { push } = useToast()

  if (bridge.identity !== 'mismatch' && bridge.identity !== 'unsigned') return null

  const relink = () => {
    const base = bridge.local.data?.baseUrl
    if (!base) {
      push({
        title: 'Desktop offline',
        detail: 'Start Nexus Desktop from the Start Menu, then try again.',
        tone: 'danger',
      })
      return
    }
    void daemonApi
      .startBrowserLogin(base)
      .then(() =>
        push({
          title: 'Sign-in opened',
          detail: 'Complete login in the browser — use the same account as this portal.',
          tone: 'teal',
        }),
      )
      .catch((err) =>
        push({
          title: 'Could not start desktop login',
          detail: err instanceof Error ? err.message : String(err),
          tone: 'danger',
        }),
      )
  }

  if (bridge.identity === 'unsigned') {
    return (
      <div className="mb-6 flex flex-wrap items-start gap-3 rounded-lg border border-amber/40 bg-amber/10 px-4 py-3 text-sm text-fg">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber" />
        <div className="min-w-0 flex-1">
          <p className="font-medium">Desktop is not signed in</p>
          <p className="mt-1 text-fg-dim">
            Nexus Desktop is running on this machine but has no account. Sign in with the{' '}
            <span className="text-fg">same</span> Nexus user as this portal (
            <span className="font-mono">{bridge.webUsername ?? 'you'}</span>) before harvest can
            sync here.
          </p>
        </div>
        <Button type="button" size="sm" onClick={relink}>
          Sign in Desktop
        </Button>
      </div>
    )
  }

  return (
    <div className="mb-6 flex flex-wrap items-start gap-3 rounded-lg border border-danger/40 bg-danger/10 px-4 py-3 text-sm text-fg">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-danger" />
      <div className="min-w-0 flex-1">
        <p className="font-medium">Different accounts on Desktop and portal</p>
        <p className="mt-1 text-fg-dim">
          Desktop is signed in as{' '}
          <span className="font-mono text-fg">{bridge.desktopUsername || bridge.desktopUserId}</span>
          , but this portal is{' '}
          <span className="font-mono text-fg">{bridge.webUsername || bridge.webUserId}</span>. Local
          harvest and file access are blocked so one account cannot see the other&apos;s workspace.
        </p>
        <p className="mt-2 text-xs text-muted">
          Fix: sign Desktop in as {bridge.webUsername || 'this portal user'}, or log out of the
          portal and use the Desktop account.
        </p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" onClick={relink}>
          Re-link Desktop
        </Button>
        <a
          className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border px-3 text-xs text-fg-dim hover:text-fg"
          href={bridge.local.data?.baseUrl || 'http://127.0.0.1:7272/'}
          target="_blank"
          rel="noreferrer"
        >
          Desktop status <ExternalLink size={12} />
        </a>
      </div>
    </div>
  )
}

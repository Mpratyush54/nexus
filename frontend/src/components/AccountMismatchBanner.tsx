import { AlertTriangle } from 'lucide-react'
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
        title: 'Desktop app not running',
        detail: 'Open Nexus from the Start menu, then try again.',
        tone: 'danger',
      })
      return
    }
    void daemonApi
      .startBrowserLogin(base)
      .then(() =>
        push({
          title: 'Sign-in opened',
          detail: 'Finish signing in in the browser, with the same account as this page.',
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
          <p className="font-medium">The desktop app isn’t signed in</p>
          <p className="mt-1 text-fg-dim">
            Nexus is open on this computer but not signed in. Sign it in with the{' '}
            <span className="text-fg">same</span> account as this page (
            <span className="font-mono">{bridge.webUsername ?? 'you'}</span>) so your chats show up
            here.
          </p>
        </div>
        <Button type="button" size="sm" onClick={relink}>
          Sign in the desktop app
        </Button>
      </div>
    )
  }

  return (
    <div className="mb-6 flex flex-wrap items-start gap-3 rounded-lg border border-danger/40 bg-danger/10 px-4 py-3 text-sm text-fg">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-danger" />
      <div className="min-w-0 flex-1">
        <p className="font-medium">The desktop app uses a different account</p>
        <p className="mt-1 text-fg-dim">
          The desktop app is signed in as{' '}
          <span className="font-mono text-fg">{bridge.desktopUsername || bridge.desktopUserId}</span>
          , but this page is{' '}
          <span className="font-mono text-fg">{bridge.webUsername || bridge.webUserId}</span>. Chats
          from this computer stay hidden here so one account can&apos;t see the other&apos;s work.
        </p>
        <p className="mt-2 text-xs text-muted">
          To fix it, sign the desktop app in as {bridge.webUsername || 'this account'}, or sign out here
          and use the desktop app&apos;s account.
        </p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" onClick={relink}>
          Sign in again
        </Button>
      </div>
    </div>
  )
}

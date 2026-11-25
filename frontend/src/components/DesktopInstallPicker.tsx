import { Check, ChevronRight, Copy, Download } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/Button'
import { useToast } from '@/components/ui/Toast'
import { DESKTOP_COMING_SOON, DESKTOP_DOWNLOAD_URL, DESKTOP_INSTALL_PS1 } from '@/lib/desktop-install'

type Props = {
  className?: string
  title?: string
  description?: string
}

export function DesktopInstallPicker({
  className,
  title = 'Install Nexus for Windows',
  description = 'Download the installer and run it. Nexus starts when you sign in to Windows.',
}: Props) {
  const { push } = useToast()
  const [copied, setCopied] = useState(false)

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(DESKTOP_INSTALL_PS1)
    } catch {
      push({ title: 'Could not copy', detail: 'Select the command and copy it by hand.' })
      return
    }
    setCopied(true)
    push({ title: 'Copied', detail: 'Paste it into PowerShell' })
    window.setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className={['space-y-4', className].filter(Boolean).join(' ')}>
      {title || description ? (
        <div>
          {title ? <h2 className="text-sm font-medium text-fg">{title}</h2> : null}
          {description ? <p className="mt-1 text-xs text-fg-dim">{description}</p> : null}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center gap-3">
        <a
          href={DESKTOP_DOWNLOAD_URL}
          target="_blank"
          rel="noreferrer"
          className="inline-flex h-10 items-center gap-2 rounded-lg bg-accent px-4 text-sm font-medium text-ink hover:opacity-90"
        >
          <Download size={16} aria-hidden />
          Download for Windows
        </a>
        <span className="text-xs text-muted">Windows 10 or 11, 64-bit · free beta</span>
      </div>

      <ul className="flex flex-wrap gap-2" aria-label="Other platforms">
        {DESKTOP_COMING_SOON.map((os) => (
          <li
            key={os}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border px-2.5 py-1 text-xs text-muted"
          >
            {os}
            <span className="rounded bg-raised px-1.5 py-px text-[10px] font-medium text-fg-dim">Coming soon</span>
          </li>
        ))}
      </ul>

      <details className="group rounded-lg border border-border">
        <summary className="flex cursor-pointer list-none items-center gap-1.5 px-3 py-2 text-xs text-fg-dim hover:text-fg [&::-webkit-details-marker]:hidden">
          <ChevronRight size={14} className="transition-transform group-open:rotate-90" aria-hidden />
          Install from a terminal instead
        </summary>
        <div className="space-y-3 border-t border-border px-3 py-3">
          <p className="text-xs text-fg-dim">Paste this into PowerShell. It runs the same installer and adds the <code className="font-mono">nexus</code> command.</p>
          <pre className="overflow-x-auto rounded-lg border border-border bg-base/50 p-3 font-mono text-[11px] leading-relaxed break-all whitespace-pre-wrap text-fg sm:whitespace-pre sm:break-normal">
            {DESKTOP_INSTALL_PS1}
          </pre>
          <Button type="button" size="sm" variant={copied ? 'confirm' : 'outline'} onClick={() => void copy()}>
            {copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}
            <span aria-live="polite">{copied ? 'Copied to clipboard' : 'Copy command'}</span>
          </Button>
        </div>
      </details>
    </div>
  )
}

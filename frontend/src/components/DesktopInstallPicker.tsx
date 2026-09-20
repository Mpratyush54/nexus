import { Check, Copy } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/Button'
import { useToast } from '@/components/ui/Toast'
import {
  DESKTOP_INSTALL_OPTIONS,
  detectDesktopOS,
  type DesktopOS,
} from '@/lib/desktop-install'

type Props = {
  className?: string
  title?: string
  description?: string
}

export function DesktopInstallPicker({
  className,
  title = 'Install Nexus Desktop',
  description = 'Pick your OS. One command installs the tray app, daemon, and CLI.',
}: Props) {
  const { push } = useToast()
  const [os, setOs] = useState<DesktopOS>(() => detectDesktopOS())
  const [copied, setCopied] = useState(false)
  const selected = DESKTOP_INSTALL_OPTIONS.find((o) => o.id === os) ?? DESKTOP_INSTALL_OPTIONS[0]

  const copy = async () => {
    await navigator.clipboard.writeText(selected.command)
    setCopied(true)
    push({ title: 'Copied', detail: `Paste into ${selected.shell}` })
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div className={['space-y-3', className].filter(Boolean).join(' ')}>
      {title || description ? (
        <div>
          {title ? <h2 className="text-sm font-medium text-fg">{title}</h2> : null}
          {description ? <p className="mt-1 text-xs text-fg-dim">{description}</p> : null}
        </div>
      ) : null}
      <div className="flex flex-wrap gap-2" role="tablist" aria-label="Operating system">
        {DESKTOP_INSTALL_OPTIONS.map((opt) => {
          const active = opt.id === os
          return (
            <button
              key={opt.id}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => setOs(opt.id)}
              className={[
                'rounded-lg border px-3 py-1.5 text-xs transition',
                active
                  ? 'border-amber bg-amber/10 text-fg'
                  : 'border-border text-fg-dim hover:border-border-strong hover:text-fg',
              ].join(' ')}
            >
              <span className="font-medium">{opt.label}</span>
              <span className="ml-1.5 text-muted">{opt.hint.split('·')[0]?.trim()}</span>
            </button>
          )
        })}
      </div>
      <p className="text-[11px] text-muted">{selected.hint}</p>
      <pre className="overflow-x-auto rounded-lg border border-border bg-base/50 p-3 font-mono text-[11px] leading-relaxed break-all whitespace-pre-wrap text-fg sm:whitespace-pre sm:break-normal">
        {selected.command}
      </pre>
      <Button type="button" size="sm" onClick={() => void copy()}>
        {copied ? <Check size={14} /> : <Copy size={14} />}
        {copied ? 'Copied' : 'Copy install command'}
      </Button>
    </div>
  )
}

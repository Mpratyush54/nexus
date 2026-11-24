import { useMemo, useState } from 'react'
import { Check, Copy, X } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { AccessibleModal } from '@/components/ui/AccessibleModal'
import { FieldRow, FormMessage, SettingsSection } from '@/components/settings/SettingsKit'
import {
  buildUserReport,
  errorReportsEnabled,
  isDemo,
  sendReport,
  setErrorReportsEnabled,
  type DiagnosticReport,
} from '@/lib/diagnostics'
import { storage } from '@/utils/storage'

/**
 * Privacy & support: the automatic error report switch and "Report a
 * problem". Automatic reports are on by default because they carry no
 * content and no account link; the copy here says exactly what they hold.
 */
export function SupportSection({ username }: { username?: string }) {
  const [enabled, setEnabled] = useState(errorReportsEnabled)
  const [open, setOpen] = useState(false)

  const toggle = () => {
    const next = !enabled
    setErrorReportsEnabled(next)
    setEnabled(next)
  }

  return (
    <SettingsSection
      id="support"
      title="Privacy & support"
      description="Help us find and fix problems. You choose what is sent."
    >
      <FieldRow
        label="Send error reports"
        id="error-reports"
        help={
          <>
            When a screen crashes or the app hits an unexpected error, send the error message, where in the code it happened,
            the app version, your browser and OS, the page path, and the last few requests the app made (method, path, status).
            Never chats, memories, page text, tokens or anything after a “?” in a link. Reports aren’t linked to your account and are
            deleted after 30 days.
          </>
        }
      >
        <div className="flex items-center gap-3 py-1">
          <button
            type="button"
            role="switch"
            aria-checked={enabled}
            aria-labelledby="error-reports-label"
            aria-describedby="error-reports-help"
            onClick={toggle}
            className={[
              'relative h-6 w-10 shrink-0 rounded-full border transition',
              enabled ? 'border-teal/40 bg-teal-soft' : 'border-border bg-raised',
            ].join(' ')}
          >
            <span className={['absolute top-0.5 h-4 w-4 rounded-full transition', enabled ? 'left-5 bg-teal' : 'left-0.5 bg-muted'].join(' ')} />
          </button>
          <span className="text-sm text-fg-dim" aria-live="polite">
            {enabled ? 'On in this browser' : 'Off in this browser'}
          </span>
        </div>
      </FieldRow>
      <FieldRow
        label="Report a problem"
        help="Describe what went wrong. You’ll see everything that will be sent before you send it, and get a report ID to quote."
      >
        <Button type="button" variant="secondary" onClick={() => setOpen(true)}>
          Report a problem…
        </Button>
      </FieldRow>
      {open ? <ReportProblemDialog username={username} onClose={() => setOpen(false)} /> : null}
    </SettingsSection>
  )
}

type Sent = { report_id: string }

export function ReportProblemDialog({ username, onClose }: { username?: string; onClose: () => void }) {
  const [note, setNote] = useState('')
  const [includeLogs, setIncludeLogs] = useState(true)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [sent, setSent] = useState<Sent | null>(null)
  const [copied, setCopied] = useState(false)
  const demo = isDemo()

  // Rebuilt as the note changes so the preview is exactly what Send posts.
  // The ID and timestamp are fixed for the dialog so a retry is stored once.
  const [fixed] = useState(() => {
    const r = buildUserReport('', { includeLogs: true })
    return { report_id: r.report_id, timestamp: r.timestamp }
  })
  const report: DiagnosticReport = useMemo(
    () => ({ ...buildUserReport(note, { includeLogs }), ...fixed }),
    [note, includeLogs, fixed],
  )
  const preview = useMemo(() => JSON.stringify(report, null, 2), [report])
  const ready = note.trim().length >= 3 && !sending && !demo

  const send = async () => {
    setSending(true)
    setError(null)
    try {
      const res = await sendReport(report, { token: storage.getToken() })
      setSent({ report_id: res.report_id })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not send the report.')
    } finally {
      setSending(false)
    }
  }

  const copyId = () => {
    if (!sent) return
    void navigator.clipboard
      .writeText(sent.report_id)
      .then(() => setCopied(true))
      .catch(() => undefined)
  }

  return (
    <AccessibleModal labelledBy="report-problem-title" onClose={onClose} className="report-dialog">
      <header className="mb-3 flex items-center justify-between gap-3">
        <h3 id="report-problem-title" className="text-lg font-semibold text-fg">
          {sent ? 'Report sent' : 'Report a problem'}
        </h3>
        <button type="button" aria-label="Close" onClick={onClose} className="rounded-md p-1 text-fg-dim hover:bg-raised hover:text-fg">
          <X size={18} />
        </button>
      </header>

      {sent ? (
        <div className="space-y-4" role="status">
          <p className="text-sm text-fg-dim">Thanks. The Nexus team can see it now. If you contact support, quote this report ID:</p>
          <div className="flex min-w-0 items-center gap-2">
            <code className="min-w-0 flex-1 truncate rounded-lg border border-border bg-raised px-3 py-2 font-mono text-sm text-fg" data-testid="report-id">
              {sent.report_id}
            </code>
            <Button type="button" size="sm" variant="secondary" onClick={copyId} aria-label={copied ? 'Copied' : 'Copy report ID'}>
              {copied ? <Check className="h-3.5 w-3.5" aria-hidden /> : <Copy className="h-3.5 w-3.5" aria-hidden />}
              {copied ? 'Copied' : 'Copy'}
            </Button>
          </div>
          <div className="flex justify-end">
            <Button type="button" onClick={onClose}>
              Done
            </Button>
          </div>
        </div>
      ) : (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (ready) void send()
          }}
        >
          <div>
            <label htmlFor="report-note" className="block text-sm font-medium text-fg">
              What went wrong?
            </label>
            <textarea
              id="report-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              rows={4}
              maxLength={4000}
              placeholder="What you were doing, what you expected, and what happened instead."
              className="mt-1.5 w-full resize-y rounded-lg border border-border bg-raised px-3 py-2 text-sm text-fg outline-none placeholder:text-muted focus:border-amber"
            />
            <p className="mt-1 text-xs text-fg-dim">Don’t paste passwords or private content. Emails, tokens and home folders are removed automatically.</p>
          </div>

          <label className="flex items-start gap-2 text-sm text-fg">
            <input type="checkbox" className="mt-0.5" checked={includeLogs} onChange={(e) => setIncludeLogs(e.target.checked)} />
            <span>
              Include the app’s recent request log
              <span className="block text-xs text-fg-dim">Method, path, status and time of the last requests in this tab. No request or response content.</span>
            </span>
          </label>

          <div>
            <p className="text-sm font-medium text-fg">Exactly what will be sent</p>
            <p className="mt-0.5 text-xs text-fg-dim">
              {username ? `Linked to your account (@${username}) so we can follow up. ` : ''}Kept for 30 days.
            </p>
            <pre
              aria-label="Report preview"
              className="mt-2 max-h-60 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-border bg-raised/60 p-3 font-mono text-[11px] leading-relaxed text-fg-dim"
            >
              {preview}
            </pre>
          </div>

          {demo ? <FormMessage kind="error">Reports aren’t sent from the demo.</FormMessage> : null}
          {error ? <FormMessage kind="error">{error}</FormMessage> : null}

          <div className="flex flex-wrap justify-end gap-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!ready}>
              {sending ? 'Sending…' : 'Send report'}
            </Button>
          </div>
        </form>
      )}
    </AccessibleModal>
  )
}

import { useState } from 'react'
import { ChevronLeft } from 'lucide-react'
import { GlassPanel } from '@/components/ui/GlassPanel'
import { StatusPill } from '@/components/ui/StatusPill'
import { LoadError, RowSkeleton } from '@/components/settings/SettingsKit'
import { useProblemGroups, useProblemReports } from '@/hooks/useAdmin'
import type { ProblemGroup, ProblemReport } from '@/api/diagnostics'
import { formatRelative } from '@/utils/format'

const SOURCES = ['', 'web', 'desktop', 'core', 'cli'] as const
const KINDS = ['', 'crash', 'error', 'user_report'] as const
const KIND_LABEL: Record<string, string> = { crash: 'Crash', error: 'Error', user_report: 'User report' }
const KIND_TONE = { crash: 'danger', error: 'amber', user_report: 'teal' } as const

const selectClass = 'h-9 rounded-lg border border-border bg-raised px-2.5 text-sm text-fg outline-none focus:border-amber'

/** Platform → Problem reports: grouped by fingerprint, newest first, with details. */
export function ProblemReportsPanel() {
  const [source, setSource] = useState('')
  const [kind, setKind] = useState('')
  const [open, setOpen] = useState<ProblemGroup | null>(null)
  const groups = useProblemGroups(source, kind, !open)

  if (open) return <ProblemDetail group={open} onBack={() => setOpen(null)} />

  return (
    <GlassPanel className="p-0">
      <div className="flex flex-wrap items-end justify-between gap-3 border-b border-border p-5">
        <div>
          <h2 className="text-sm font-medium text-fg">Problem reports</h2>
          <p className="mt-0.5 text-xs text-fg-dim">
            Crashes, errors and user reports from the web app, desktop app, core and CLI. Same problem, same row. Kept{' '}
            {groups.data?.retention_days ?? 30} days.
          </p>
        </div>
        <div className="flex gap-2">
          <label className="sr-only" htmlFor="problem-source">
            Source
          </label>
          <select id="problem-source" className={selectClass} value={source} onChange={(e) => setSource(e.target.value)}>
            {SOURCES.map((s) => (
              <option key={s} value={s}>
                {s ? s : 'All sources'}
              </option>
            ))}
          </select>
          <label className="sr-only" htmlFor="problem-kind">
            Kind
          </label>
          <select id="problem-kind" className={selectClass} value={kind} onChange={(e) => setKind(e.target.value)}>
            {KINDS.map((k) => (
              <option key={k} value={k}>
                {k ? KIND_LABEL[k] : 'All kinds'}
              </option>
            ))}
          </select>
        </div>
      </div>

      {groups.isError ? (
        <div className="p-5">
          <LoadError what="problem reports" onRetry={() => void groups.refetch()} />
        </div>
      ) : !groups.data ? (
        <div className="p-5">
          <RowSkeleton lines={4} />
        </div>
      ) : groups.data.items.length === 0 ? (
        <p className="p-5 text-sm text-fg-dim">No problem reports{source || kind ? ' match these filters' : ' in the last 30 days'}.</p>
      ) : (
        <ul className="divide-y divide-border" aria-label="Problem groups">
          {groups.data.items.map((g) => (
            <li key={g.fingerprint}>
              <button
                type="button"
                onClick={() => setOpen(g)}
                className="grid w-full gap-1 px-5 py-3.5 text-left transition hover:bg-raised/60 md:grid-cols-[minmax(0,1fr)_auto] md:gap-6"
              >
                <span className="min-w-0">
                  <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
                    <StatusPill tone={KIND_TONE[g.kind] ?? 'neutral'}>{KIND_LABEL[g.kind] ?? g.kind}</StatusPill>
                    <StatusPill>{g.source}</StatusPill>
                    <span className="font-mono text-[11px] text-muted">{g.versions.join(', ') || 'no version'}</span>
                  </span>
                  <span className="mt-1 block truncate text-sm text-fg">{g.message || '(no message)'}</span>
                </span>
                <span className="flex items-center gap-5 text-xs text-fg-dim md:justify-end">
                  <span>
                    <strong className="font-semibold tabular-nums text-fg">{g.count}</strong> {g.count === 1 ? 'report' : 'reports'}
                  </span>
                  <span>
                    <strong className="font-semibold tabular-nums text-fg">{g.users}</strong> {g.users === 1 ? 'account' : 'accounts'}
                  </span>
                  <span title={`First seen ${new Date(g.first_seen).toLocaleString()}`}>last {formatRelative(g.last_seen)}</span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </GlassPanel>
  )
}

function ProblemDetail({ group, onBack }: { group: ProblemGroup; onBack: () => void }) {
  const reports = useProblemReports(group.fingerprint)
  return (
    <div className="space-y-4">
      <button type="button" onClick={onBack} className="inline-flex items-center gap-1 text-sm text-fg-dim hover:text-fg">
        <ChevronLeft size={16} /> All problem reports
      </button>
      <GlassPanel className="space-y-3 p-5">
        <div className="flex flex-wrap items-center gap-3">
          <StatusPill tone={KIND_TONE[group.kind] ?? 'neutral'}>{KIND_LABEL[group.kind] ?? group.kind}</StatusPill>
          <StatusPill>{group.source}</StatusPill>
          <span className="font-mono text-[11px] text-muted">fingerprint {group.fingerprint}</span>
        </div>
        <h2 className="break-words text-base font-semibold text-fg">{group.message || '(no message)'}</h2>
        <dl className="grid gap-3 text-sm sm:grid-cols-4">
          <Stat label="Reports" value={String(group.count)} />
          <Stat label="Accounts" value={String(group.users)} />
          <Stat label="First seen" value={formatRelative(group.first_seen)} />
          <Stat label="Last seen" value={formatRelative(group.last_seen)} />
        </dl>
        <p className="text-xs text-fg-dim">Versions: {group.versions.join(', ') || 'none reported'}</p>
      </GlassPanel>

      {reports.isError ? (
        <LoadError what="reports" onRetry={() => void reports.refetch()} />
      ) : !reports.data ? (
        <RowSkeleton lines={4} />
      ) : (
        <div className="space-y-3">
          <p className="text-xs text-fg-dim">Newest {reports.data.length} reports</p>
          {reports.data.map((r, i) => (
            <ReportCard key={r.report_id} report={r} defaultOpen={i === 0} />
          ))}
        </div>
      )}
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-border bg-raised/50 px-3 py-2">
      <dt className="text-[11px] text-fg-dim">{label}</dt>
      <dd className="text-sm font-medium text-fg">{value}</dd>
    </div>
  )
}

function ReportCard({ report: r, defaultOpen }: { report: ProblemReport; defaultOpen: boolean }) {
  const context = Object.entries(r.context ?? {})
  return (
    <GlassPanel className="p-0">
      <details open={defaultOpen} className="group">
        <summary className="flex cursor-pointer list-none flex-wrap items-center justify-between gap-2 px-5 py-3 text-sm">
          <span className="font-mono text-xs text-fg">{r.report_id}</span>
          <span className="text-xs text-fg-dim">
            {r.app_version || 'no version'} · {r.os || 'unknown OS'} · {formatRelative(r.received_at)}
          </span>
        </summary>
        <div className="space-y-3 border-t border-border px-5 py-4 text-sm">
          <dl className="grid gap-x-6 gap-y-1 text-xs sm:grid-cols-[8rem_minmax(0,1fr)]">
            <dt className="text-fg-dim">Account</dt>
            <dd className="font-mono text-fg">{r.user_id || 'anonymous'}</dd>
            <dt className="text-fg-dim">Upload request</dt>
            <dd className="font-mono text-fg">{r.request_id || '—'}</dd>
            <dt className="text-fg-dim">Client time</dt>
            <dd className="text-fg">{new Date(r.timestamp).toLocaleString()}</dd>
            {context.map(([k, v]) => (
              <Pair key={k} k={k} v={v} />
            ))}
          </dl>
          {r.user_note ? <Block title="User note" text={r.user_note} /> : null}
          <Block title="Message" text={r.message} />
          {r.stack ? <Block title="Stack" text={r.stack} mono /> : null}
          {r.logs && r.logs.length > 0 ? <Block title={`Recent log (${r.logs.length} lines)`} text={r.logs.join('\n')} mono /> : null}
        </div>
      </details>
    </GlassPanel>
  )
}

function Pair({ k, v }: { k: string; v: string }) {
  return (
    <>
      <dt className="text-fg-dim">{k}</dt>
      <dd className="break-all font-mono text-fg">{v}</dd>
    </>
  )
}

function Block({ title, text, mono }: { title: string; text: string; mono?: boolean }) {
  return (
    <div>
      <p className="mb-1 text-xs font-medium text-fg-dim">{title}</p>
      <pre
        className={[
          'max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-lg border border-border bg-base p-3 text-[11px] leading-5 text-fg',
          mono ? 'font-mono' : 'font-sans text-xs',
        ].join(' ')}
      >
        {text}
      </pre>
    </div>
  )
}

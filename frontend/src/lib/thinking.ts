/**
 * The agent's thinking on a web reply. Desktop relay jobs carry it as
 * `thinking` (the latest part, capped) and `thinking_ms` (how long the agent
 * thought before its first words). While the agent thinks the block reads
 * "Thinking… 4s" with a shimmer; once the words start, or the turn ends, it
 * collapses to "Thought for 7s".
 */

export type ThinkingJob = {
  state: string
  text?: string
  thinking?: string
  thinking_ms?: number
  result?: { text?: string }
}

const FINISHED = new Set(['completed', 'failed', 'cancelled', 'expired'])

/** Still thinking: the turn runs, there is thinking, and no words yet. */
export function thinkingLive(job: ThinkingJob): boolean {
  return !FINISHED.has(job.state) && !!job.thinking?.trim() && !(job.text || job.result?.text)?.trim()
}

/** "4s", "1m 05s": like the desktop's "Thought for" label. Never below 1s. */
export function thinkingSeconds(ms: number): string {
  const s = Math.max(1, Math.round(ms / 1000))
  if (s < 60) return `${s}s`
  return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`
}

/**
 * How long the agent has thought: the desktop's measure when it has one,
 * else what this page saw (from the first thinking it showed). A live block
 * keeps counting from the larger of the two.
 */
export function thinkingMs(job: ThinkingJob, seenSince: number | null, now: number): number {
  const reported = Math.max(0, job.thinking_ms ?? 0)
  if (!thinkingLive(job)) return reported
  const local = seenSince === null ? 0 : Math.max(0, now - seenSince)
  return Math.max(reported, local)
}

/** The block's header: "Thinking…", "Thinking… 4s", "Thought for 7s" or "Thought process". */
export function thinkingHeader(live: boolean, ms: number): string {
  if (live) return ms >= 1000 ? `Thinking… ${thinkingSeconds(ms)}` : 'Thinking…'
  return ms > 0 ? `Thought for ${thinkingSeconds(ms)}` : 'Thought process'
}

/** The last lines of the thinking, for the live preview under the header. */
export function thinkingPreview(text: string, maxChars = 240): string {
  const t = text.trim().replace(/\s+/g, ' ')
  if (t.length <= maxChars) return t
  return '…' + t.slice(t.length - maxChars + 1).replace(/^\S*\s/, '')
}

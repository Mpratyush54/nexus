import type { ChatPage, ChatTurn } from '@/api/chats'

/**
 * Pages a whole transcript the way the server's /fetch does: newest first
 * (cursor = exclusive upper idx) without a query, oldest-first matches
 * (cursor = last idx returned) with one.
 */
export function pageTurnsLocally(all: ChatTurn[], cursor: string, query: string, limit: number): ChatPage<ChatTurn> {
  const sorted = [...all].sort((a, b) => a.idx - b.idx)
  const needle = query.trim().toLowerCase()
  if (needle) {
    const after = cursor ? Number(cursor) : -Infinity
    const matches = sorted.filter((turn) => turn.idx > after && (
      (turn.text_preview ?? '').toLowerCase().includes(needle) ||
      JSON.stringify(turn.tool_calls ?? '').toLowerCase().includes(needle)))
    const items = matches.slice(0, limit)
    return { items, next_cursor: matches.length > limit ? String(items[items.length - 1].idx) : undefined }
  }
  const before = cursor ? Number(cursor) : Infinity
  const older = sorted.filter((turn) => turn.idx < before)
  const items = older.slice(Math.max(0, older.length - limit))
  return { items, next_cursor: older.length > limit ? String(items[0].idx) : undefined }
}

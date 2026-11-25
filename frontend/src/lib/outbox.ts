/**
 * A message you just sent, shown at once (before the server answers).
 * If sending fails it stays in the chat, marked "Not sent", with Retry and Edit;
 * it never vanishes or gets pushed back into the composer over new typing.
 */
import type { DraftImage } from './image-attach'

export type OutboxImage = DraftImage

export type Outgoing = {
  id: string
  text: string
  images: OutboxImage[]
  state: 'sending' | 'failed'
  error?: string
  at: number
}

export type OutboxAction =
  | { type: 'send'; id: string; text: string; images?: OutboxImage[]; at?: number }
  | { type: 'failed'; id: string; error: string }
  | { type: 'retry'; id: string }
  | { type: 'sent'; id: string }
  | { type: 'discard'; id: string }

export function outbox(state: Outgoing | null, action: OutboxAction): Outgoing | null {
  switch (action.type) {
    case 'send':
      return { id: action.id, text: action.text, images: action.images ?? [], state: 'sending', at: action.at ?? Date.now() }
    case 'failed':
      return state?.id === action.id ? { ...state, state: 'failed', error: action.error } : state
    case 'retry':
      return state?.id === action.id ? { ...state, state: 'sending', error: undefined } : state
    case 'sent':
    case 'discard':
      return state?.id === action.id ? null : state
  }
}

/** The first line of an error, so a long relay message doesn't flood the chat. */
export function shortReason(error: string): { short: string; more: string } {
  const text = error.trim()
  const cut = text.search(/\n|(?<=[.!?])\s/)
  if (cut < 0 || cut > 140) return text.length > 140 ? { short: text.slice(0, 137) + '…', more: text } : { short: text, more: '' }
  return { short: text.slice(0, cut).trim(), more: text.slice(cut).trim() }
}

/** Where an Edit puts a failed message: into an empty composer, or above what you've typed since. */
export function editIntoComposer(failedText: string, composer: string): string {
  if (!composer.trim()) return failedText
  return `${failedText}\n\n${composer}`
}

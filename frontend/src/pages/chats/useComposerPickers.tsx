import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type RefObject } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Brain, CircleHelp, Cpu, FilePlus2, Eraser, Minimize2, MessageSquare, Slash, X } from 'lucide-react'
import { chatsApi } from '@/api/chats'
import { memoryApi } from '@/api/memory'
import { ComposerPopup, type PopupRow } from '@/components/ComposerPopup'
import {
  applyMentions,
  findTrigger,
  highlights,
  parseCommand,
  rankCommands,
  replaceTrigger,
  webCommands,
  type Command,
  type Mention,
  type Trigger,
} from '@/lib/composer-commands'
import { chatTitle } from '@/lib/harness-message'
import { memoryTitle } from '@/lib/plain-language'

const COMMAND_ICONS: Record<string, typeof Slash> = {
  model: Cpu,
  new: FilePlus2,
  clear: Eraser,
  compact: Minimize2,
  help: CircleHelp,
}

type Pickable = { kind: 'command'; command: Command } | { kind: 'mention'; mention: Mention }

/**
 * "/" commands and "@" mentions for the web composer. The web has no project
 * file list, so "@" offers other chats and memories (sent along as context).
 * Returns the key handler (call it first in onKeyDown), the popup, the
 * mention chips and the /help card for ChatsPage to place.
 */
export function useComposerPickers({
  draft,
  setDraft,
  textarea,
  agent,
  sessionId,
  projectId,
  onCommand,
}: {
  draft: string
  setDraft: (text: string) => void
  textarea: RefObject<HTMLTextAreaElement | null>
  agent: string
  sessionId: string
  projectId: string
  /** Runs one of the web's commands; return false to send it as typed. */
  onCommand: (name: string, argument: string) => boolean
}) {
  const [trigger, setTrigger] = useState<Trigger | null>(null)
  const [selected, setSelected] = useState(0)
  const [mentions, setMentions] = useState<Mention[]>([])
  const [helpOpen, setHelpOpen] = useState(false)
  // Esc closes the list until the text changes again.
  const dismissed = useRef<string | null>(null)

  const updateCaret = useCallback((value: string, caret: number | null, selectionEnd?: number | null) => {
    if (dismissed.current !== null && dismissed.current === value) return
    dismissed.current = null
    const next = caret !== null && (selectionEnd ?? caret) === caret ? findTrigger(value, caret) : null
    setTrigger((prev) => {
      if (prev && next && prev.symbol === next.symbol && prev.start === next.start && prev.query === next.query) return prev
      setSelected(0)
      return next
    })
  }, [])

  // The draft can change without typing (recall, a pick, a switch): re-read the trigger.
  useEffect(() => {
    const el = textarea.current
    if (!el) return
    updateCaret(el.value, el.selectionStart, el.selectionEnd)
  }, [draft, textarea, updateCaret])

  // Chats and memories, a moment after typing stops.
  const [remoteQuery, setRemoteQuery] = useState<string | null>(null)
  useEffect(() => {
    if (trigger?.symbol !== '@') {
      setRemoteQuery(null)
      return
    }
    const q = trigger.query
    const t = window.setTimeout(() => setRemoteQuery(q), q ? 160 : 0)
    return () => window.clearTimeout(t)
  }, [trigger?.symbol, trigger?.query])
  const chats = useQuery({
    queryKey: ['mention-chats', projectId, remoteQuery],
    enabled: remoteQuery !== null && !!projectId,
    queryFn: ({ signal }) => chatsApi.list(projectId, remoteQuery ?? '', '', signal),
    staleTime: 30_000,
  })
  const memories = useQuery({
    queryKey: ['mention-memories', projectId, remoteQuery],
    enabled: remoteQuery !== null && !!projectId,
    queryFn: ({ signal }) => memoryApi.search({ projectId, q: remoteQuery || undefined, limit: 5 }, signal),
    staleTime: 30_000,
  })

  const items = useMemo((): { row: PopupRow; pick: Pickable }[] => {
    if (!trigger) return []
    if (trigger.symbol === '/') {
      return rankCommands(webCommands(agent), trigger.query).map(({ command, positions }) => {
        const Icon = COMMAND_ICONS[command.name] ?? Slash
        return {
          row: {
            id: 'cmd:' + command.name,
            icon: <Icon size={15} />,
            title: '/' + command.name,
            titleHits: positions.map((p) => p + 1),
            detail: command.description,
            group: 'Commands',
          },
          pick: { kind: 'command', command },
        }
      })
    }
    const q = trigger.query
    const list: { row: PopupRow; pick: Pickable }[] = []
    for (const card of chats.data?.items ?? []) {
      if (card.session_id === sessionId || (card.parent_session_id && card.lineage_kind === 'subagent')) continue
      const title = chatTitle(card.title, card.summary, card.harness)
      if (mentions.some((m) => m.kind === 'chat' && m.value === card.session_id)) continue
      list.push({
        row: {
          id: 'chat:' + card.session_id,
          icon: <MessageSquare size={15} />,
          title,
          titleHits: highlights(q, title),
          group: 'Chats',
        },
        pick: { kind: 'mention', mention: { kind: 'chat', value: card.session_id, label: title, context: card.summary || title } },
      })
      if (list.length >= 6) break
    }
    for (const item of memories.data?.items ?? []) {
      if (mentions.some((m) => m.kind === 'memory' && m.value === item.id)) continue
      const title = (item.key ? memoryTitle(item.key).title : '') || item.content.slice(0, 80)
      const detail = item.content.length > 90 ? item.content.slice(0, 90) + '…' : item.content
      list.push({
        row: { id: 'memory:' + item.id, icon: <Brain size={15} />, title, titleHits: highlights(q, title), detail, group: 'Memories' },
        pick: { kind: 'mention', mention: { kind: 'memory', value: item.id, label: title, context: item.content } },
      })
    }
    return list
  }, [trigger, agent, chats.data, memories.data, sessionId, mentions])

  const open = !!trigger
  const loading = trigger?.symbol === '@' && (remoteQuery !== trigger.query || chats.isFetching || memories.isFetching)

  // Keep the last list on screen for the closing fade.
  const [closing, setClosing] = useState<{ rows: PopupRow[]; title: string } | null>(null)
  const lastShown = useRef<{ rows: PopupRow[]; title: string } | null>(null)
  useEffect(() => {
    if (open) {
      lastShown.current = { rows: items.map((i) => i.row), title: trigger?.symbol === '/' ? 'Commands' : 'Mention a chat or memory' }
      setClosing(null)
      return
    }
    if (!lastShown.current) return
    const shown = lastShown.current
    lastShown.current = null
    setClosing(shown)
    const t = window.setTimeout(() => setClosing(null), 90)
    return () => window.clearTimeout(t)
  }, [open, items, trigger?.symbol])

  const close = useCallback(() => {
    dismissed.current = draft
    setTrigger(null)
  }, [draft])

  const setCaret = useCallback(
    (caret: number) =>
      requestAnimationFrame(() => {
        const el = textarea.current
        if (!el) return
        el.focus()
        el.setSelectionRange(caret, caret)
      }),
    [textarea],
  )

  const pick = useCallback(
    (index: number) => {
      const chosen = items[index]
      const el = textarea.current
      if (!chosen || !trigger || !el) return
      const caret = el.selectionStart ?? draft.length
      if (chosen.pick.kind === 'mention') {
        const mention = chosen.pick.mention
        const next = replaceTrigger(el.value, trigger, caret)
        dismissed.current = next.text
        setTrigger(null)
        setDraft(next.text)
        setMentions((list) => (list.some((m) => m.kind === mention.kind && m.value === mention.value) ? list : [...list, mention]))
        setCaret(next.caret)
        return
      }
      const command = chosen.pick.command
      setTrigger(null)
      if (command.name === 'help') {
        dismissed.current = ''
        setDraft('')
        setHelpOpen(true)
        setCaret(0)
        return
      }
      if (!onCommand(command.name, '')) {
        const text = '/' + command.name + ' '
        dismissed.current = text
        setDraft(text)
        setCaret(text.length)
        return
      }
      dismissed.current = ''
      setDraft('')
    },
    [items, trigger, textarea, draft, setDraft, onCommand, setCaret],
  )

  /** Call first in the textarea's onKeyDown; true when the key was used here. */
  const onKeyDown = useCallback(
    (e: KeyboardEvent<HTMLTextAreaElement>) => {
      if (!open || e.nativeEvent.isComposing) return false
      const plain = !e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey
      if (e.key === 'Escape') {
        e.preventDefault()
        close()
        return true
      }
      if (!items.length) return e.key === 'Tab' ? (e.preventDefault(), true) : false
      if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && plain) {
        e.preventDefault()
        setSelected((s) => (s + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length)
        return true
      }
      if ((e.key === 'Enter' || e.key === 'Tab') && plain) {
        e.preventDefault()
        pick(Math.min(selected, items.length - 1))
        return true
      }
      return false
    },
    [open, items, selected, pick, close],
  )

  /** A whole message that is one of the web's commands ("/model gpt-5") runs instead of being sent. */
  const runTyped = useCallback(
    (text: string) => {
      if (mentions.length) return false
      const parsed = parseCommand(text)
      if (!parsed) return false
      const known = webCommands(agent).find((c) => c.name === parsed.name && c.kind === 'action')
      if (!known) return false
      if (parsed.name === 'help') {
        setHelpOpen(true)
        return true
      }
      return onCommand(parsed.name, parsed.argument)
    },
    [mentions.length, agent, onCommand],
  )

  /** The prompt with the picked chats and memories as context. */
  const withMentions = useCallback((text: string) => applyMentions(text, mentions), [mentions])
  const clearMentions = useCallback(() => setMentions([]), [])

  const shown = open ? { rows: items.map((i) => i.row), title: trigger?.symbol === '/' ? 'Commands' : 'Mention a chat or memory' } : closing
  const popup = shown ? (
    <div className={`composer-popup-host ${open ? '' : 'is-closing'}`}>
      <ComposerPopup
        id={`composer-popup-${sessionId}`}
        title={shown.title}
        rows={shown.rows}
        selected={Math.min(selected, Math.max(0, shown.rows.length - 1))}
        loading={open && loading}
        empty={
          trigger?.symbol === '/'
            ? 'No commands match. Enter sends it to the agent as you typed it.'
            : trigger?.query
              ? `No chats or memories match “${trigger.query}”.`
              : 'No other chats or memories in this project yet.'
        }
        onHover={setSelected}
        onPick={pick}
      />
    </div>
  ) : null

  const chips = mentions.length ? (
    <ul className="composer-mentions" aria-label="Mentioned">
      {mentions.map((m) => (
        <li key={m.kind + m.value} title={m.kind === 'chat' ? 'Chat · sent along as context' : 'Memory · sent along as context'}>
          {m.kind === 'chat' ? <MessageSquare size={12} aria-hidden /> : <Brain size={12} aria-hidden />}
          <span>{m.label.length > 40 ? m.label.slice(0, 39) + '…' : m.label}</span>
          <button
            type="button"
            aria-label={'Remove ' + m.label}
            onClick={() => {
              setMentions((list) => list.filter((x) => !(x.kind === m.kind && x.value === m.value)))
              textarea.current?.focus()
            }}
          >
            <X size={11} />
          </button>
        </li>
      ))}
    </ul>
  ) : null

  const help = helpOpen ? (
    <div className="composer-help" role="note" aria-label="Composer help">
      <header>
        <strong>What you can type</strong>
        <button type="button" aria-label="Close help" onClick={() => setHelpOpen(false)}>
          <X size={13} />
        </button>
      </header>
      <dl>
        {webCommands(agent).map((c) => (
          <div key={c.name}>
            <dt>
              /{c.name}
              {c.takesArgument ? ' …' : ''}
            </dt>
            <dd>{c.description}</dd>
          </div>
        ))}
        <div>
          <dt>@</dt>
          <dd>Mention another chat or a memory; it goes along as context</dd>
        </div>
        <div>
          <dt>↑ / ↓</dt>
          <dd>Bring back a message you sent earlier</dd>
        </div>
        <div>
          <dt>Esc</dt>
          <dd>Stop the agent while it works</dd>
        </div>
      </dl>
    </div>
  ) : null

  return {
    open,
    activeOption: open && items.length ? `composer-popup-${sessionId}-${Math.min(selected, items.length - 1)}` : undefined,
    listId: `composer-popup-${sessionId}`,
    onKeyDown,
    updateCaret,
    popup,
    chips,
    help,
    mentions,
    clearMentions,
    withMentions,
    runTyped,
  }
}

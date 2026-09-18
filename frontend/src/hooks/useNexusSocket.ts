import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { wsUrl } from '@/lib/env'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'
import { useToast } from '@/components/ui/Toast'
import type { WsEnvelope } from '@/types/api'
import { normalizeMCPFromWs, type MCPToolCall } from '@/api/agents'

const MAX_BACKOFF_MS = 15_000

const TOAST_EVENTS: Record<string, { title: string; tone: 'teal' | 'amber' | 'neutral' }> = {
  MEMORY_PROPOSED: { title: 'Memory proposed', tone: 'amber' },
  MEMORY_CONFIRMED: { title: 'Memory confirmed', tone: 'teal' },
  MEMORY_UPDATED: { title: 'Memory edited', tone: 'neutral' },
  MEMORY_REJECTED: { title: 'Memory rejected', tone: 'amber' },
  MEMORY_SUPERSEDED: { title: 'Memory superseded', tone: 'neutral' },
  SESSION_HANDOFF_INITIATED: { title: 'Handoff started', tone: 'amber' },
  SESSION_HANDOFF_ACCEPTED: { title: 'Handoff accepted', tone: 'teal' },
}

export function useNexusSocket() {
  const { token, projectId, isAuthenticated } = useAuth()
  const qc = useQueryClient()
  const { push } = useToast()
  const [status, setStatus] = useState<'idle' | 'connecting' | 'open' | 'closed'>('idle')
  const backoffRef = useRef(1000)

  useEffect(() => {
    if (!isAuthenticated || !token || !projectId) {
      setStatus('idle')
      return
    }

    let stopped = false
    let socket: WebSocket | null = null
    let timer: number | undefined

    const connect = () => {
      if (stopped) return
      setStatus('connecting')
      socket = new WebSocket(wsUrl(token))

      socket.onopen = () => {
        backoffRef.current = 1000
        setStatus('open')
        // Hub only fans out after subscribe (see internal/server/ws.go).
        socket?.send(
          JSON.stringify({
            type: 'subscribe',
            project_id: projectId,
          }),
        )
        socket?.send(
          JSON.stringify({
            type: 'presence',
            status: 'online',
            project_id: projectId,
          }),
        )
      }

      socket.onmessage = (ev) => {
        try {
          const msg = JSON.parse(String(ev.data)) as WsEnvelope
          const eventType = msg.event_type
          if (
            msg.type === 'memory_update' ||
            msg.type === 'event' ||
            eventType === 'MEMORY_PROPOSED' ||
            eventType === 'MEMORY_CONFIRMED' ||
            eventType === 'MEMORY_REJECTED' ||
            eventType === 'MEMORY_UPDATED'
          ) {
            void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
            void qc.invalidateQueries({ queryKey: ['activity'] })
            void qc.invalidateQueries({ queryKey: ['notifications'] })
          }
          if (msg.type === 'memory_update' && msg.action) {
            const mapped = {
              proposed: 'MEMORY_PROPOSED',
              confirmed: 'MEMORY_CONFIRMED',
              rejected: 'MEMORY_REJECTED',
              updated: 'MEMORY_UPDATED',
              superseded: 'MEMORY_SUPERSEDED',
            }[msg.action]
            const copy = mapped ? TOAST_EVENTS[mapped] : undefined
            const item = msg.item
            if (copy) {
              push({
                title: copy.title,
                detail: item?.key,
                tone: copy.tone,
              })
            }
          } else if (eventType && TOAST_EVENTS[eventType] && !eventType.startsWith('MEMORY_')) {
            const copy = TOAST_EVENTS[eventType]
            const payload = msg.payload as { key?: string; tool_name?: string } | undefined
            push({
              title: copy.title,
              detail: payload?.key || payload?.tool_name,
              tone: copy.tone,
            })
          }
          if (msg.type === 'presence') {
            void qc.invalidateQueries({ queryKey: ['workspace'] })
            void qc.invalidateQueries({ queryKey: ['daemon'] })
            void qc.invalidateQueries({ queryKey: queryKeys.workspace.presence(projectId) })
          }
          if (
            msg.type === 'event' &&
            (msg.event_type === 'WORKSPACE_LOCAL' || msg.event_type === 'workspace_local')
          ) {
            void qc.invalidateQueries({ queryKey: ['daemon'] })
            void qc.invalidateQueries({ queryKey: ['workspace'] })
          }
          if (
            msg.type === 'event' &&
            (msg.event_type === 'MCP_TOOL_CALL' ||
              (msg.payload &&
                typeof msg.payload === 'object' &&
                (msg.payload as { event_type?: string }).event_type === 'MCP_TOOL_CALL'))
          ) {
            const call = normalizeMCPFromWs({
              event_type: 'MCP_TOOL_CALL',
              user_id: msg.user_id,
              payload: msg.payload,
            })
            if (call) {
              qc.setQueryData<MCPToolCall[]>(queryKeys.agents.mcpFeed(projectId), (old) => {
                const prev = old ?? []
                const id = String(call.id)
                if (prev.some((c) => String(c.id) === id)) return prev
                return [call, ...prev].slice(0, 80)
              })
            }
          }
        } catch {
          // ignore malformed frames
        }
      }

      socket.onclose = () => {
        setStatus('closed')
        if (stopped) return
        const wait = backoffRef.current
        backoffRef.current = Math.min(backoffRef.current * 1.8, MAX_BACKOFF_MS)
        timer = window.setTimeout(connect, wait)
      }

      socket.onerror = () => {
        socket?.close()
      }
    }

    connect()

    return () => {
      stopped = true
      if (timer) window.clearTimeout(timer)
      if (socket && socket.readyState === WebSocket.OPEN) {
        try {
          socket.send(
            JSON.stringify({
              type: 'presence',
              status: 'offline',
              project_id: projectId,
            }),
          )
        } catch {
          // closing anyway
        }
      }
      socket?.close()
    }
  }, [isAuthenticated, token, projectId, qc, push])

  return { status }
}

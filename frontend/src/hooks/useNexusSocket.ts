import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { wsUrl } from '@/lib/env'
import { queryKeys } from '@/lib/query-keys'
import { useAuth } from '@/providers/AuthProvider'
import type { WsEnvelope } from '@/types/api'

const MAX_BACKOFF_MS = 15_000

export function useNexusSocket() {
  const { token, projectId, isAuthenticated } = useAuth()
  const qc = useQueryClient()
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
          if (
            msg.type === 'memory_update' ||
            msg.type === 'event' ||
            msg.event_type === 'MEMORY_PROPOSED' ||
            msg.event_type === 'MEMORY_CONFIRMED' ||
            msg.event_type === 'MEMORY_REJECTED' ||
            msg.event_type === 'MEMORY_UPDATED'
          ) {
            void qc.invalidateQueries({ queryKey: queryKeys.memory.all })
          }
          if (msg.type === 'presence') {
            void qc.invalidateQueries({ queryKey: ['workspace'] })
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
  }, [isAuthenticated, token, projectId, qc])

  return { status }
}

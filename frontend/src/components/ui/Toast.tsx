import { AnimatePresence, motion } from 'framer-motion'
import { X } from 'lucide-react'
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'
import { refForToast } from '@/lib/diagnostics'

type Toast = {
  id: string
  title: string
  detail?: string
  tone?: 'neutral' | 'teal' | 'amber' | 'danger'
  /**
   * Server request ID to quote in a problem report. Error toasts pick it up
   * from the API failure they describe when it is not given.
   */
  ref?: string
}

type ToastApi = {
  push: (toast: Omit<Toast, 'id'>) => void
}

const ToastContext = createContext<ToastApi | null>(null)

export function useToast() {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast must be used within ToastProvider')
  return ctx
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const push = useCallback((toast: Omit<Toast, 'id'>) => {
    const id = crypto.randomUUID()
    const ref = toast.ref ?? (toast.tone === 'danger' ? refForToast(toast.detail ?? toast.title) : undefined)
    setToasts((prev) => [...prev, { ...toast, ref, id }])
    // Long enough to read or copy the reference.
    window.setTimeout(() => {
      setToasts((prev) => prev.filter((t) => t.id !== id))
    }, ref ? 9000 : 3200)
  }, [])

  const api = useMemo(() => ({ push }), [push])

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="pointer-events-none fixed bottom-6 right-6 z-[80] flex w-80 flex-col gap-2">
        <AnimatePresence>
          {toasts.map((t) => (
            <motion.div
              key={t.id}
              initial={{ opacity: 0, y: 12, scale: 0.96 }}
              animate={{ opacity: 1, y: 0, scale: 1 }}
              exit={{ opacity: 0, y: 8, scale: 0.96 }}
              transition={{ type: 'spring', stiffness: 420, damping: 28 }}
              className="pointer-events-auto glass flex items-start gap-3 rounded-xl border border-border px-3.5 py-3"
            >
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-fg">{t.title}</p>
                {t.detail ? (
                  <p className="mt-0.5 truncate text-xs text-fg-dim">{t.detail}</p>
                ) : null}
                {t.ref ? (
                  <p className="mt-1 select-all font-mono text-[11px] text-muted" title="Quote this reference if you report the problem">
                    Something went wrong — ref {t.ref}
                  </p>
                ) : null}
              </div>
              <button
                type="button"
                className="text-muted hover:text-fg"
                onClick={() => setToasts((prev) => prev.filter((x) => x.id !== t.id))}
                aria-label="Dismiss"
              >
                <X size={14} />
              </button>
            </motion.div>
          ))}
        </AnimatePresence>
      </div>
    </ToastContext.Provider>
  )
}

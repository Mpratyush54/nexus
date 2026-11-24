import { useEffect, useRef, type ReactNode } from 'react'

// Native dialog provides focus trapping, Escape and focus restoration. A
// close event that arrives while the dialog is open again (React StrictMode
// re-runs the effect) must not dismiss it.
export function AccessibleModal({ children, onClose, labelledBy, className = 'chat-modal' }: { children: ReactNode; onClose: () => void; labelledBy: string; className?: string }) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => { const dialog = ref.current; dialog?.showModal(); return () => dialog?.close() }, [])
  return <dialog ref={ref} aria-labelledby={labelledBy} className={className} onCancel={onClose} onClose={() => { if (!ref.current?.open) onClose() }}>{children}</dialog>
}

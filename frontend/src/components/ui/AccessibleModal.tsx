import { useEffect, useRef, type ReactNode } from 'react'

// Native dialog provides focus trapping, Escape and focus restoration.
export function AccessibleModal({ children, onClose, labelledBy }: { children: ReactNode; onClose: () => void; labelledBy: string }) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => { const dialog = ref.current; dialog?.showModal(); return () => dialog?.close() }, [])
  return <dialog ref={ref} aria-labelledby={labelledBy} className="chat-modal" onCancel={onClose} onClose={onClose}>{children}</dialog>
}

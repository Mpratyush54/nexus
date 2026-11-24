import type { ReactNode } from 'react'
import { AnimatePresence, motion, useReducedMotion } from 'framer-motion'

/** Cross-fades between the steps of one page (details → code, request → reset). */
export function StepSwap({ step, children }: { step: string; children: ReactNode }) {
  const reduce = useReducedMotion()
  return (
    <AnimatePresence mode="wait" initial={false}>
      <motion.div
        key={step}
        initial={reduce ? false : { opacity: 0, x: 12 }}
        animate={{ opacity: 1, x: 0 }}
        exit={reduce ? { opacity: 0, transition: { duration: 0 } } : { opacity: 0, x: -12 }}
        transition={{ duration: 0.18, ease: [0.22, 1, 0.36, 1] }}
      >
        {children}
      </motion.div>
    </AnimatePresence>
  )
}

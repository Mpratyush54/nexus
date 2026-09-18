import { motion } from 'framer-motion'
import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { useAuth } from '@/providers/AuthProvider'

export function LandingPage() {
  const { isAuthenticated } = useAuth()

  return (
    <div className="mesh-bg relative min-h-svh">
      <header className="relative z-10 mx-auto flex max-w-4xl items-center justify-between px-5 py-6">
        <span className="text-[15px] font-semibold tracking-tight text-fg">Nexus</span>
        <div className="flex items-center gap-1">
          {isAuthenticated ? (
            <Link to="/app/memory">
              <Button size="sm">Open app</Button>
            </Link>
          ) : (
            <>
              <Link to="/login">
                <Button variant="ghost" size="sm">
                  Log in
                </Button>
              </Link>
              <Link to="/signup">
                <Button size="sm">Sign up</Button>
              </Link>
            </>
          )}
        </div>
      </header>

      <section className="relative z-10 mx-auto flex min-h-[calc(100svh-5rem)] max-w-4xl flex-col justify-center px-5 pb-24">
        <motion.div
          initial={{ opacity: 0, y: 12 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.45, ease: [0.22, 1, 0.36, 1] }}
        >
          <p className="font-mono text-xs text-amber">central memory</p>
          <h1 className="mt-4 max-w-xl text-[clamp(2.75rem,8vw,4.5rem)] font-semibold leading-[1.05] tracking-tight text-fg">
            One place your team and agents remember.
          </h1>
          <p className="mt-5 max-w-md text-[15px] leading-relaxed text-fg-dim">
            Propose, review, and sync project memory in real time — across editors, daemons, and
            people.
          </p>
          <div className="mt-9 flex flex-wrap items-center gap-3">
            <Link to={isAuthenticated ? '/app/memory' : '/login'}>
              <Button size="lg">{isAuthenticated ? 'Open app' : 'Get started'}</Button>
            </Link>
            {!isAuthenticated ? (
              <Link to="/signup">
                <Button variant="secondary" size="lg">
                  Sign up
                </Button>
              </Link>
            ) : null}
          </div>
        </motion.div>
      </section>
    </div>
  )
}

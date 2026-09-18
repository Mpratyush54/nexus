import { motion, useScroll, useTransform } from 'framer-motion'
import { useRef } from 'react'
import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { PlanGrid } from '@/components/PlanGrid'
import { useBillingPlans } from '@/hooks/useBilling'
import { useAuth } from '@/providers/AuthProvider'

const STORY = [
  {
    id: 'memory',
    kicker: '01 — remember',
    title: 'Every decision lands in one place.',
    copy: 'Agents and teammates propose memories. You review, confirm, or reject — then the project keeps the truth.',
    image: '/landing/memory.png',
    alt: 'Nexus memory review queue',
  },
  {
    id: 'team',
    kicker: '02 — connect',
    title: 'Pull the people who already know the repo.',
    copy: 'Connect GitHub, pick a repository, import collaborators. Roles map themselves. Open the repo when you need the source.',
    image: '/landing/team.png',
    alt: 'Nexus team and GitHub import',
  },
  {
    id: 'dashboard',
    kicker: '03 — see',
    title: 'A year of project memory at a glance.',
    copy: 'Contribution heat, confirmed vs proposed, agent calls, who’s online — the same pulse you expect from a good repo, for shared memory.',
    image: '/landing/dashboard.png',
    alt: 'Nexus activity dashboard',
  },
] as const

function Screen({ src, alt, priority }: { src: string; alt: string; priority?: boolean }) {
  return (
    <div className="landing-screen">
      <img
        src={src}
        alt={alt}
        width={1600}
        height={900}
        loading={priority ? 'eager' : 'lazy'}
        decoding="async"
        className="landing-screen-img"
      />
    </div>
  )
}

export function LandingPage() {
  const { isAuthenticated } = useAuth()
  const plans = useBillingPlans()
  const heroRef = useRef<HTMLElement>(null)
  const { scrollYProgress } = useScroll({
    target: heroRef,
    offset: ['start start', 'end start'],
  })
  const heroY = useTransform(scrollYProgress, [0, 1], [0, 80])
  const heroDim = useTransform(scrollYProgress, [0, 1], [1, 0.85])

  return (
    <div className="landing-root">
      <header className="landing-nav">
        <span className="landing-brand">Nexus</span>
        <div className="flex items-center gap-1">
          {isAuthenticated ? (
            <Link to="/app/dashboard">
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

      <section ref={heroRef} className="landing-hero">
        <motion.div style={{ y: heroY, opacity: heroDim }} className="landing-hero-copy">
          <p className="landing-kicker">central memory</p>
          <h1 className="landing-title">Nexus</h1>
          <p className="landing-lede">
            Shared memory for the team and the agents that ship with you.
          </p>
          <div className="mt-8 flex flex-wrap gap-3">
            <Link to={isAuthenticated ? '/app/dashboard' : '/signup'}>
              <Button size="lg">{isAuthenticated ? 'Open app' : 'Start free'}</Button>
            </Link>
            <a href="#story">
              <Button variant="secondary" size="lg">
                See the product
              </Button>
            </a>
          </div>
        </motion.div>
        <motion.div
          className="landing-hero-visual"
          initial={{ opacity: 0, y: 24 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.7, delay: 0.12, ease: [0.22, 1, 0.36, 1] }}
        >
          <Screen src="/landing/dashboard.png" alt="Nexus dashboard" priority />
        </motion.div>
      </section>

      <section id="story" className="landing-story">
        {STORY.map((beat, i) => (
          <motion.article
            key={beat.id}
            className={`landing-beat ${i % 2 === 1 ? 'is-flip' : ''}`}
            initial={{ opacity: 0, y: 28 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true, margin: '-10% 0px' }}
            transition={{ duration: 0.55, ease: [0.22, 1, 0.36, 1] }}
          >
            <div className="landing-beat-copy">
              <p className="landing-kicker">{beat.kicker}</p>
              <h2 className="landing-beat-title">{beat.title}</h2>
              <p className="landing-beat-body">{beat.copy}</p>
            </div>
            <Screen src={beat.image} alt={beat.alt} />
          </motion.article>
        ))}
      </section>

      <section className="landing-plans">
        <p className="landing-kicker">plans</p>
        <h2 className="landing-beat-title">Free to start. Room when the org grows.</h2>
        <div className="mt-8">
          {plans.data ? <PlanGrid plans={plans.data} highlight="free" canChange={false} /> : null}
        </div>
        <div className="mt-10">
          <Link to={isAuthenticated ? '/app/dashboard' : '/signup'}>
            <Button size="lg">{isAuthenticated ? 'Open app' : 'Create account'}</Button>
          </Link>
        </div>
      </section>

      <footer className="landing-foot">
        <span className="landing-brand">Nexus</span>
        <span className="text-xs text-muted">central memory for humans and agents</span>
      </footer>
    </div>
  )
}

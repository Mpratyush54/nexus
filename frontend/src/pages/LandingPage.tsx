import { motion, useScroll, useTransform } from 'framer-motion'
import { Volume2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { PlanGrid } from '@/components/PlanGrid'
import { useBillingPlans } from '@/hooks/useBilling'
import { useAuth } from '@/providers/AuthProvider'

const STAGES = [
  {
    id: 'shared',
    eyebrow: 'Shared memory',
    title: 'One living record for people and agents.',
    copy: 'Agents propose. Teammates confirm, redirect, and hand off. The project keeps what mattered — not a thousand private threads nobody else can touch.',
    image: '/landing/memory.png',
    alt: 'Nexus shared memory library',
  },
  {
    id: 'agents',
    eyebrow: 'Agents at work',
    title: 'Drop into the same session.',
    copy: 'Desktop harvests Cursor and OpenCode. Agents mint their own MCP credentials. Anyone on the team can watch, steer, and continue — the way they would with a human teammate.',
    image: '/landing/dashboard.png',
    alt: 'Nexus agent dashboard',
  },
  {
    id: 'team',
    eyebrow: 'Multiplayer by default',
    title: 'Built for crowds around one problem.',
    copy: 'Import collaborators from GitHub, set roles, and share context across engineers, PMs, and agents — so work that takes hours or weeks never lives in a box only one person can see.',
    image: '/landing/team.png',
    alt: 'Nexus team sharing',
  },
] as const

export function LandingPage() {
  const { isAuthenticated } = useAuth()
  const plans = useBillingPlans()
  const stageRef = useRef<HTMLElement>(null)
  const videoRef = useRef<HTMLVideoElement>(null)
  const [soundOn, setSoundOn] = useState(false)
  const [playing, setPlaying] = useState(false)

  const { scrollYProgress } = useScroll({
    target: stageRef,
    offset: ['start end', 'end start'],
  })
  const videoScale = useTransform(scrollYProgress, [0.1, 0.45], [0.92, 1])
  const videoOpacity = useTransform(scrollYProgress, [0, 0.15], [0.55, 1])

  const primaryTo = isAuthenticated ? '/app/dashboard' : '/signup'
  const primaryLabel = isAuthenticated ? 'Open Nexus' : 'Get started'

  useEffect(() => {
    const el = videoRef.current
    if (!el) return
    const onPlay = () => setPlaying(true)
    const onPause = () => setPlaying(false)
    el.addEventListener('play', onPlay)
    el.addEventListener('pause', onPause)
    // Browsers allow muted autoplay — start the stage film quietly until sound is requested.
    el.muted = true
    void el.play().catch(() => undefined)
    return () => {
      el.removeEventListener('play', onPlay)
      el.removeEventListener('pause', onPause)
    }
  }, [])

  const startFilmWithSound = async () => {
    const el = videoRef.current
    if (!el) return
    el.currentTime = 0
    el.muted = false
    el.volume = 1
    setSoundOn(true)
    try {
      await el.play()
    } catch {
      setSoundOn(false)
      el.muted = true
    }
  }

  const toggleMute = async () => {
    const el = videoRef.current
    if (!el) return
    if (el.muted) {
      el.muted = false
      el.volume = 1
      setSoundOn(true)
      if (el.paused) await el.play().catch(() => undefined)
    } else {
      el.muted = true
      setSoundOn(false)
    }
  }

  return (
    <div className="landing-root">
      <div className="landing-bg" aria-hidden="true">
        <video
          className="landing-bg-video"
          src="/landing/brag.mp4"
          poster="/landing/brag.jpg"
          autoPlay
          muted
          loop
          playsInline
          preload="auto"
        />
      </div>
      <header className="landing-nav">
        <Link to="/" className="landing-brand" aria-label="Nexus home">
          Nexus
        </Link>
        <nav className="landing-nav-links" aria-label="Primary">
          <a href="#film">Film</a>
          <a href="#product">Product</a>
          <a href="#pricing">Pricing</a>
          {isAuthenticated ? (
            <Link to="/app/dashboard">
              <Button size="sm">{primaryLabel}</Button>
            </Link>
          ) : (
            <>
              <Link to="/login" className="landing-nav-text">
                Log in
              </Link>
              <Link to="/signup">
                <Button size="sm">Get started</Button>
              </Link>
            </>
          )}
        </nav>
      </header>

      <section className="landing-intro" aria-label="Hero">
        <motion.p
          className="landing-intro-eyebrow"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          transition={{ duration: 0.6 }}
        >
          Nexus
        </motion.p>
        <motion.h1
          className="landing-intro-title"
          initial={{ opacity: 0, y: 16 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.7, ease: [0.22, 1, 0.36, 1] }}
        >
          Your AI shipped. Your team never got the context.
        </motion.h1>
        <motion.p
          className="landing-intro-lede"
          initial={{ opacity: 0, y: 12 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.55, delay: 0.08, ease: [0.22, 1, 0.36, 1] }}
        >
          Code is multiplayer. Design is multiplayer. Agents still aren’t. Nexus is the shared
          memory layer so everyone — humans and agents — finally has the same context.
        </motion.p>
        <motion.div
          className="landing-intro-cta"
          initial={{ opacity: 0, y: 10 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.5, delay: 0.16 }}
        >
          <Link to={primaryTo}>
            <Button size="lg">{primaryLabel}</Button>
          </Link>
          <a href="#film" className="landing-link">
            Watch the film
          </a>
        </motion.div>
      </section>

      <section id="film" ref={stageRef} className="landing-stage" aria-label="Product film">
        <motion.div className="landing-stage-frame" style={{ scale: videoScale, opacity: videoOpacity }}>
          <video
            ref={videoRef}
            className="landing-stage-video"
            src="/landing/brag.mp4"
            poster="/landing/brag.jpg"
            muted
            loop
            playsInline
            preload="auto"
            controls={playing && soundOn}
            aria-label="Nexus product film"
          />
          {!soundOn ? (
            <button type="button" className="landing-stage-play" onClick={() => void startFilmWithSound()}>
              <span className="landing-stage-play-icon" aria-hidden />
              <span className="landing-stage-play-label">Play with sound</span>
            </button>
          ) : (
            <button
              type="button"
              className="landing-stage-mute"
              onClick={() => void toggleMute()}
              aria-label="Mute film"
            >
              <Volume2 size={16} />
              Sound on
            </button>
          )}
        </motion.div>
        <p className="landing-stage-caption">Playing muted in the background. Tap for sound.</p>
      </section>

      <section className="landing-thesis" aria-label="The problem">
        <p className="landing-feature-eyebrow">The problem</p>
        <h2 className="landing-thesis-title">
          The best tools of the last two decades won by going multiplayer.
        </h2>
        <p className="landing-thesis-body">
          Google Docs replaced Word. Figma beat Photoshop. Solo tools became places where teams do
          their best work together. AI hasn’t had that moment yet — agents are the most powerful new
          tool a team has, and the one thing people still use alone. You open a chat, type a prompt,
          get an answer in a box only you can see. Collaborate, and the best you can do is send a
          read-only transcript nobody else can touch.
        </p>
        <p className="landing-thesis-body landing-thesis-turn">That’s about to change.</p>
        <p className="landing-thesis-body">
          Agents run tasks that take hours, days, even weeks. Work at that scale was never meant to
          be done alone. Anyone on a team should drop into the same living session — watch it work,
          redirect it, hand it off — the way they’d work with any other teammate. Nexus turns what
          your team does with agents into a shared, living record instead of a thousand private
          threads.
        </p>
      </section>

      <section id="product" className="landing-stages">
        {STAGES.map((stage, i) => (
          <motion.article
            key={stage.id}
            className={`landing-feature ${i % 2 === 1 ? 'is-flip' : ''}`}
            initial={{ opacity: 0, y: 28 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true, margin: '-10% 0px' }}
            transition={{ duration: 0.55, ease: [0.22, 1, 0.36, 1] }}
          >
            <div className="landing-feature-copy">
              <p className="landing-feature-eyebrow">{stage.eyebrow}</p>
              <h2 className="landing-feature-title">{stage.title}</h2>
              <p className="landing-feature-body">{stage.copy}</p>
            </div>
            <div className="landing-feature-media">
              <img src={stage.image} alt={stage.alt} width={1400} height={788} loading="lazy" />
            </div>
          </motion.article>
        ))}
      </section>

      <section id="pricing" className="landing-pricing">
        <p className="landing-feature-eyebrow">Pricing</p>
        <h2 className="landing-pricing-title">Start free.</h2>
        <p className="landing-pricing-lede">
          Paid upgrades open when checkout ships. Until then, the free plan is yours.
        </p>
        <div className="landing-pricing-grid">
          {plans.data ? (
            <PlanGrid plans={plans.data} highlight="free" canChange={false} />
          ) : (
            <p className="text-sm text-muted">Loading plans…</p>
          )}
        </div>
        <div className="landing-pricing-cta">
          <Link to={primaryTo}>
            <Button size="lg">{isAuthenticated ? 'Open Nexus' : 'Create free account'}</Button>
          </Link>
        </div>
      </section>

      <footer className="landing-foot">
        <span className="landing-brand">Nexus</span>
        <span className="landing-foot-meta">Multiplayer memory for humans and agents</span>
        <div className="landing-foot-links">
          <Link to="/login">Log in</Link>
          <Link to="/signup">Sign up</Link>
        </div>
      </footer>
    </div>
  )
}

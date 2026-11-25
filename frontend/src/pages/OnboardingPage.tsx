import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, ArrowRight, Check, CircleCheck, FolderGit2, LogIn, TriangleAlert } from 'lucide-react'
import { DesktopInstallPicker } from '@/components/DesktopInstallPicker'
import { useTrustedLocalBridge } from '@/hooks/useTrustedLocalBridge'
import { StepSwap } from '@/auth/StepSwap'
import '@/auth/auth.css'
import { useAuth } from '@/providers/AuthProvider'
import { storage } from '@/utils/storage'

/**
 * Optional first-run guide. Never forced (AppShell only offers it), every
 * step can be skipped, and finishing or skipping lands on Home.
 */
export function OnboardingPage() {
  const { user } = useAuth()
  const bridge = useTrustedLocalBridge()
  const navigate = useNavigate()
  const [step, setStep] = useState(0)
  const name = user?.username ?? 'you'

  const finish = () => {
    storage.setOnboardingDone(true)
    navigate('/app/dashboard', { replace: true })
  }

  const steps = [
    {
      title: 'Get the desktop app',
      lede: 'Nexus Desktop runs quietly in your tray and saves what your coding agents learn. Copy the command for your system and run it once.',
      body: <DesktopInstallPicker title="" description="" />,
    },
    {
      title: 'Sign in on the desktop',
      lede: `Open Nexus and choose Sign in. Use this same account (${name}) so your memory stays in one place.`,
      body: (
        <>
          <ul className="au-ob-list">
            <li>
              <LogIn size={16} aria-hidden />
              <span>
                <strong>Windows:</strong> Start menu → Nexus, or the tray icon.
              </span>
            </li>
            <li>
              <LogIn size={16} aria-hidden />
              <span>
                <strong>macOS / Linux:</strong> Applications or your app launcher.
              </span>
            </li>
          </ul>
          {bridge.identity === 'match' ? (
            <p className="au-ob-status">
              <CircleCheck size={16} aria-hidden /> Nexus Desktop on this computer is signed in as you.
            </p>
          ) : bridge.identity === 'mismatch' ? (
            <p className="au-ob-status is-warn">
              <TriangleAlert size={16} aria-hidden /> Nexus Desktop is signed in to a different account.
              Fix it later on Desktop setup.
            </p>
          ) : null}
        </>
      ),
    },
    {
      title: 'Open a project',
      lede: 'In Nexus Desktop, pick a project folder. From then on, chats with Claude Code, Codex, Cursor and the rest are saved to that project’s memory.',
      body: (
        <ul className="au-ob-list">
          <li>
            <FolderGit2 size={16} aria-hidden />
            <span>Your agents can then look up earlier decisions instead of starting from zero.</span>
          </li>
          <li>
            <Check size={16} aria-hidden />
            <span>You can browse it all here on the web, from any device.</span>
          </li>
        </ul>
      ),
    },
  ]
  const current = steps[step]
  const last = step === steps.length - 1

  return (
    <div className="au au-embedded">
      <div className="au-ob">
        <div className="au-ob-top">
          <div className="au-ob-progress" role="progressbar" aria-label="Setup progress" aria-valuemin={1} aria-valuemax={steps.length} aria-valuenow={step + 1}>
            {steps.map((s, i) => (
              <i key={s.title} className={i <= step ? 'is-on' : ''} />
            ))}
          </div>
          <button type="button" className="au-link is-quiet" onClick={finish}>
            Skip setup
          </button>
        </div>

        <div className="au-head">
          <p className="au-kicker">welcome, {name}</p>
          <h1 className="au-title">Set up Nexus in three short steps</h1>
          <p className="au-sub">Optional. You can use the web app right away and come back to this from Desktop setup.</p>
        </div>

        <div className="au-ob-card">
          <StepSwap step={String(step)}>
            <p className="au-ob-step">
              step {step + 1} of {steps.length}
            </p>
            <h2 className="au-title" style={{ fontSize: 24, marginTop: 8 }}>
              {current.title}
            </h2>
            <p className="au-sub">{current.lede}</p>
            <div style={{ marginTop: 18 }}>{current.body}</div>
          </StepSwap>
          <div className="au-ob-nav">
            {step > 0 ? (
              <button type="button" className="au-link is-quiet" onClick={() => setStep(step - 1)}>
                <ArrowLeft size={14} aria-hidden style={{ verticalAlign: '-2px', marginRight: 4 }} />
                Back
              </button>
            ) : (
              <span />
            )}
            <button type="button" className="au-btn" onClick={() => (last ? finish() : setStep(step + 1))}>
              {last ? 'Go to Home' : 'Next'} <ArrowRight size={16} aria-hidden />
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

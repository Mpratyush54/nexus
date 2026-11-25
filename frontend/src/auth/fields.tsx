import {
  useId,
  useState,
  type InputHTMLAttributes,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { AnimatePresence, motion, useReducedMotion } from 'framer-motion'
import { AlertCircle, Check, Eye, EyeOff, Info, TriangleAlert } from 'lucide-react'
import type { FriendlyError } from './errors'
import { passwordScore } from './validation'

type FieldProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'onChange' | 'value'> & {
  label: string
  value: string
  onValue: (value: string) => void
  /** Returns a message when the value is not acceptable yet. */
  validate?: (value: string) => string | null
  /** The form was submitted: show every field's problem, touched or not. */
  showErrors?: boolean
  aside?: ReactNode
  hint?: ReactNode
  /** A line shown under the field instead of the hint (e.g. caps lock). */
  warning?: string | null
  code?: boolean
  trailing?: ReactNode
  below?: ReactNode
}

export function Field({
  label,
  value,
  onValue,
  validate,
  showErrors = false,
  aside,
  hint,
  warning,
  code = false,
  trailing,
  below,
  className = '',
  onBlur,
  ...input
}: FieldProps) {
  const id = useId()
  const msgId = `${id}-msg`
  const [touched, setTouched] = useState(false)
  const problem = validate ? validate(value) : null
  const showProblem = Boolean(problem) && (showErrors || (touched && value.length > 0))

  let msg: ReactNode = hint ?? null
  let tone = ''
  if (showProblem) {
    msg = (
      <>
        <AlertCircle size={14} aria-hidden />
        {problem}
      </>
    )
    tone = 'is-error'
  } else if (warning) {
    msg = (
      <>
        <TriangleAlert size={14} aria-hidden />
        {warning}
      </>
    )
    tone = 'is-warn'
  }

  return (
    <div className="au-field">
      <div className="au-label-row">
        <label className="au-label" htmlFor={id}>
          {label}
        </label>
        {aside}
      </div>
      <div className="au-control">
        <input
          id={id}
          value={value}
          onChange={(e) => onValue(e.target.value)}
          onBlur={(e) => {
            setTouched(true)
            onBlur?.(e)
          }}
          aria-invalid={showProblem || undefined}
          aria-describedby={msg ? msgId : undefined}
          className={['au-input', code ? 'is-code' : '', trailing ? 'has-toggle' : '', className].join(' ')}
          {...input}
        />
        {trailing}
      </div>
      {below}
      <p id={msgId} className={`au-msg ${tone}`} aria-live={showProblem ? 'polite' : undefined}>
        {msg}
      </p>
    </div>
  )
}

type PasswordProps = Omit<FieldProps, 'type' | 'trailing' | 'warning'> & {
  strength?: { others: string[] } | null
}

export function PasswordField({ strength, onKeyUp, onKeyDown, onBlur, hint, ...props }: PasswordProps) {
  const [shown, setShown] = useState(false)
  const [caps, setCaps] = useState(false)
  const track = (e: KeyboardEvent<HTMLInputElement>) => {
    if (typeof e.getModifierState === 'function') setCaps(e.getModifierState('CapsLock'))
  }
  const score = strength ? passwordScore(props.value, strength.others) : null

  return (
    <Field
      {...props}
      type={shown ? 'text' : 'password'}
      spellCheck={false}
      autoCapitalize="none"
      autoCorrect="off"
      onKeyDown={(e) => {
        track(e)
        onKeyDown?.(e)
      }}
      onKeyUp={(e) => {
        track(e)
        onKeyUp?.(e)
      }}
      onBlur={(e) => {
        setCaps(false)
        onBlur?.(e)
      }}
      warning={caps ? 'Caps Lock is on' : null}
      hint={
        score && props.value ? (
          <>
            {score.score >= 3 ? <Check size={14} aria-hidden /> : <Info size={14} aria-hidden />}
            <span>
              <strong style={{ fontWeight: 600 }}>{score.label}.</strong> {score.tip}
            </span>
          </>
        ) : (
          hint
        )
      }
      below={
        score ? (
          <div className="au-strength" data-score={props.value ? score.score : 0} aria-hidden>
            <i />
            <i />
            <i />
            <i />
          </div>
        ) : null
      }
      trailing={
        <button
          type="button"
          className="au-toggle"
          onClick={() => setShown((v) => !v)}
          aria-label={shown ? 'Hide password' : 'Show password'}
          aria-pressed={shown}
        >
          {shown ? <EyeOff size={18} aria-hidden /> : <Eye size={18} aria-hidden />}
        </button>
      }
    />
  )
}

export function SubmitButton({
  busy,
  done = false,
  children,
  busyLabel,
  doneLabel = 'Done',
  type = 'submit',
  variant,
  onClick,
  disabled,
}: {
  busy: boolean
  done?: boolean
  children: ReactNode
  busyLabel: string
  doneLabel?: string
  type?: 'submit' | 'button'
  variant?: 'secondary'
  onClick?: () => void
  disabled?: boolean
}) {
  return (
    <button
      type={type}
      className={['au-btn', done ? 'is-ok' : '', variant === 'secondary' ? 'is-secondary' : ''].join(' ')}
      aria-busy={busy || undefined}
      // Busy buttons ignore clicks without dropping focus or changing size.
      onClick={(e) => {
        if (busy || done) {
          e.preventDefault()
          return
        }
        onClick?.()
      }}
      disabled={disabled}
    >
      <span className="au-btn-label">
        {done ? (
          <>
            <Check size={18} aria-hidden /> {doneLabel}
          </>
        ) : busy ? (
          <>
            <span className="au-spinner" aria-hidden /> {busyLabel}
          </>
        ) : (
          children
        )}
      </span>
    </button>
  )
}

/** Animated, announced message above a form. */
export function FormAlert({
  error,
  tone = 'error',
  children,
}: {
  error?: FriendlyError | null
  tone?: 'error' | 'info' | 'ok'
  children?: ReactNode
}) {
  const reduce = useReducedMotion()
  const show = Boolean(error) || Boolean(children)
  const key = error ? `${error.title}|${error.detail ?? ''}` : 'note'
  return (
    <AnimatePresence initial={false}>
      {show ? (
        <motion.div
          key={key}
          initial={reduce ? false : { opacity: 0, height: 0 }}
          animate={{ opacity: 1, height: 'auto' }}
          exit={reduce ? { opacity: 0, transition: { duration: 0 } } : { opacity: 0, height: 0 }}
          transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}
          style={{ overflow: 'hidden' }}
        >
          <div
            className={`au-alert ${tone === 'info' ? 'is-info' : tone === 'ok' ? 'is-ok' : ''}`}
            role={tone === 'error' ? 'alert' : 'status'}
          >
            {tone === 'error' ? (
              <AlertCircle size={18} aria-hidden />
            ) : tone === 'ok' ? (
              <Check size={18} aria-hidden />
            ) : (
              <Info size={18} aria-hidden />
            )}
            <div>
              {error ? (
                <>
                  <strong>{error.title}</strong>
                  {error.detail ? <p>{error.detail}</p> : null}
                </>
              ) : null}
              {children}
            </div>
          </div>
        </motion.div>
      ) : null}
    </AnimatePresence>
  )
}


// The onboarding shell: the village behind, the header with the progress rail,
// and the glass panel holding one step with Back / Skip for now / Continue.
// The shell owns the step's eyebrow and h1 (focused on every step change); a
// step renders what goes under the heading. Steps mark their own progress:
// Continue and Back never write anything, Skip writes the skip.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import Scene from './Scene.jsx'
import Rail from './Rail.jsx'
import { stepsFor, statusesOf, stepTitle, railLabel } from './steps.js'
import Placeholder from './steps/Placeholder.jsx'
import Welcome from './steps/Welcome.jsx'
import SystemCheck from './steps/SystemCheck.jsx'
import CreateProfile from './steps/CreateProfile.jsx'
import AboutYou from './steps/AboutYou.jsx'
import Monomind from './steps/Monomind.jsx'
import Agents from './steps/Agents.jsx'
import Browser from './steps/Browser.jsx'
import Permissions from './steps/Permissions.jsx'
import FirstRun from './steps/FirstRun.jsx'
import Finish from './steps/Finish.jsx'
import './onboarding.css'

// One entry per step id; each step task replaces its own line.
export const STEP_COMPONENTS = {
  welcome: Welcome,
  system: SystemCheck,
  profile: CreateProfile,
  about: AboutYou,
  monomind: Monomind,
  agents: Agents,
  browser: Browser,
  permissions: Permissions,
  firstRun: FirstRun,
  finish: Finish,
}

const NO_ENTER = new Set(['BUTTON', 'A', 'TEXTAREA', 'SELECT'])
const TEXT_INPUTS = ['text', 'email', 'search', 'url', 'tel', 'number', 'password', '']
const OWN_ENTER_ROLES = ['button', 'link', 'checkbox', 'radio', 'switch', 'option', 'tab', 'menuitem', 'combobox', 'listbox', 'slider', 'textbox']

// Enter continues only from inside the panel, outside any dialog, from the
// panel itself or a text field, or with focus on the page itself (after a
// click on the village or on panel text) while no dialog is open; never from a
// button (it clicks itself), a link, a select, a textarea, a non-text input, a
// control with its own Enter (an ARIA role), a field of a form (the form
// submits), or a held key.
function enterContinues(e, panel) {
  if (e.key !== 'Enter' || e.repeat || e.defaultPrevented || e.isComposing || e.shiftKey || e.metaKey || e.ctrlKey || e.altKey) return false
  const el = e.target
  if (!panel || !el || !el.tagName) return false
  if (el === el.ownerDocument.body) return !el.ownerDocument.querySelector('[role="dialog"]')
  if (!panel.contains(el) || el.closest('[role="dialog"]')) return false
  if (NO_ENTER.has(el.tagName) || el.isContentEditable || el.form) return false
  if (el.tagName === 'INPUT' && !TEXT_INPUTS.includes(el.type)) return false
  if (el.tagName !== 'INPUT' && OWN_ENTER_ROLES.includes(el.getAttribute('role'))) return false
  return true
}

const Brand = () => (
  <svg viewBox="0 0 32 32" aria-hidden="true">
    <circle cx="16" cy="16" r="15" fill="oklch(0.22 0.06 290)" stroke="oklch(0.83 0.15 72 / .6)" />
    <path d="M8 22v-7l8-6.5 8 6.5v7z" fill="oklch(0.83 0.15 72)" />
    <rect x="14" y="16" width="4" height="6" rx="1" fill="oklch(0.22 0.05 60)" />
    <circle cx="23.5" cy="8.5" r="1.6" fill="oklch(0.95 0.05 85)" />
  </svg>
)

function startIndex(steps, want, statuses) {
  const i = steps.findIndex(s => s.id === want)
  if (i >= 0) return i
  const todo = steps.findIndex(s => statuses[s.id] === 'todo')
  return todo >= 0 ? todo : 0
}

export default function Onboarding({ state, startStep, mode = 'full', onFinish, reload, mark, skip, complete, banners = null }) {
  const { t } = useTranslation()
  const steps = useMemo(() => stepsFor(mode), [mode])
  const statuses = useMemo(() => statusesOf(state), [state])
  const [index, setIndex] = useState(() => startIndex(steps, startStep || state?.currentStep, statuses))
  const [canContinue, setCanContinueState] = useState(false)
  const [label, setLabel] = useState(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const indexRef = useRef(index)
  const busyRef = useRef(false)
  const handlerRef = useRef(null)
  const titleRef = useRef(null)
  const bodyRef = useRef(null)
  const panelRef = useRef(null)
  const finishedRef = useRef(false)
  const finishRef = useRef(onFinish)
  finishRef.current = onFinish

  const step = steps[Math.min(index, steps.length - 1)]

  // Moving resets what the step registered, here and not in an effect: the
  // new step's own effects run before the shell's and would be wiped.
  const goIndex = useCallback((i) => {
    const next = Math.max(0, Math.min(steps.length - 1, i))
    if (next === indexRef.current) return
    indexRef.current = next
    finishedRef.current = false
    handlerRef.current = null
    setCanContinueState(false)
    setLabel(null)
    setError('')
    setIndex(next)
  }, [steps.length])

  // Finishing happens once: a handler that finished (onDone or onFinish on
  // the last step) is not followed by Continue's own finish without its nav.
  const finish = useCallback((nav) => {
    if (finishedRef.current) return
    finishedRef.current = true
    if (finishRef.current) finishRef.current(nav)
  }, [])
  const advance = useCallback((nav) => {
    if (indexRef.current >= steps.length - 1) finish(nav)
    else goIndex(indexRef.current + 1)
  }, [steps.length, finish, goIndex])
  const goTo = useCallback((id) => {
    const i = steps.findIndex(s => s.id === id)
    if (i >= 0) goIndex(i)
  }, [steps, goIndex])
  const back = useCallback(() => { if (!busyRef.current) goIndex(indexRef.current - 1) }, [goIndex])

  const setCanContinue = useCallback((v) => setCanContinueState(!!v), [])
  const setOnContinue = useCallback((fn) => { handlerRef.current = typeof fn === 'function' ? fn : null }, [])
  const setContinueLabel = useCallback((text) => setLabel(text || null), [])

  const working = async (fn) => {
    busyRef.current = true
    setBusy(true)
    try { return await fn() } finally { busyRef.current = false; setBusy(false) }
  }

  const doContinue = async () => {
    if (!canContinue || busyRef.current) return
    const at = indexRef.current
    const handler = handlerRef.current
    if (handler) {
      setError('')
      let ok
      try {
        ok = await working(handler)
      } catch (e) {
        setError(e?.message ?? String(e))
        return
      }
      // false, or a callStep failure (shown here), keeps the step
      if (ok && ok.ok === false) { setError(ok.message); return }
      if (ok === false || finishedRef.current || indexRef.current !== at) return
    }
    advance()
  }

  const doSkip = async () => {
    if (busyRef.current || !step.skippable) return
    const at = indexRef.current
    setError('')
    const r = await working(() => Promise.resolve(skip ? skip(step.id) : undefined))
    if (r && r.ok === false) { setError(r.message); return }
    if (indexRef.current === at) advance()
  }

  const continueRef = useRef(doContinue)
  continueRef.current = doContinue
  useEffect(() => {
    const onKey = (e) => {
      if (!enterContinues(e, panelRef.current)) return
      e.preventDefault()
      continueRef.current()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])

  // A new step: back to the top of the panel, focus on its heading.
  useEffect(() => {
    if (bodyRef.current) bodyRef.current.scrollTop = 0
    if (titleRef.current) titleRef.current.focus({ preventScroll: true })
  }, [index])

  const Step = STEP_COMPONENTS[step.id] || Placeholder
  const name = railLabel(t, step.id)
  const total = steps.length
  const onDone = advance

  return (
    <div className="onb-root" data-mode={mode}>
      <Scene statuses={statuses} currentStep={step.id} />
      <div className="onb-layer">
        {banners && <div className="onb-banners">{banners}</div>}
        <header className="onb-top">
          <div className="onb-brand">
            <Brand />
            <div>
              {t('onboarding.shell.brand')}
              <small>{mode === 'profile' ? t('onboarding.shell.subtitleProfile') : t('onboarding.shell.subtitle')}</small>
            </div>
          </div>
          <Rail steps={steps} statuses={statuses} index={index} />
        </header>
        <main className="onb-stage">
          <section className="onb-panel" aria-labelledby="onb-step-title" ref={panelRef}>
            <div className="onb-panel-body" ref={bodyRef}>
              <div className="onb-step-in" key={step.id}>
                <p className="onb-eyebrow">{t('onboarding.shell.eyebrow', { n: index + 1, total, name })}</p>
                <h1 id="onb-step-title" className="onb-h1" tabIndex={-1} ref={titleRef}>{stepTitle(t, step.id)}</h1>
                <Step
                  key={step.id}
                  state={state}
                  profileId={state?.profileId || ''}
                  mode={mode}
                  reload={reload}
                  mark={mark}
                  skip={skip}
                  complete={complete}
                  onDone={onDone}
                  onFinish={finish}
                  goTo={goTo}
                  setCanContinue={setCanContinue}
                  setOnContinue={setOnContinue}
                  setContinueLabel={setContinueLabel}
                />
              </div>
            </div>
            <footer className="onb-panel-foot">
              {error && <p className="onb-foot-error" role="alert">{error}</p>}
              <button type="button" className="onb-btn ghost" onClick={back} disabled={index === 0 || busy} style={index === 0 ? { visibility: 'hidden' } : undefined}>
                {t('onboarding.shell.back')}
              </button>
              <span className="onb-sp" />
              {step.skippable && (
                <button type="button" className="onb-btn ghost" onClick={doSkip} disabled={busy}>{t('onboarding.shell.skip')}</button>
              )}
              <button type="button" className="onb-btn primary" onClick={doContinue} disabled={!canContinue || busy} aria-busy={busy || undefined}>
                {label || t('onboarding.shell.continue')}
              </button>
            </footer>
          </section>
        </main>
        <div className="onb-sr" aria-live="polite">{t('onboarding.shell.announce', { n: index + 1, total, name })}</div>
      </div>
    </div>
  )
}

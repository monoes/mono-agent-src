import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { createScene, STEP_IDS } from './scene/index.js'

// The tag over the plot the current step builds.
function planLabel(t, id) {
  switch (id) {
    case 'welcome': return t('onboarding.scene.plan.welcome')
    case 'system': return t('onboarding.scene.plan.system')
    case 'profile': return t('onboarding.scene.plan.profile')
    case 'about': return t('onboarding.scene.plan.about')
    case 'monomind': return t('onboarding.scene.plan.monomind')
    case 'agents': return t('onboarding.scene.plan.agents')
    case 'browser': return t('onboarding.scene.plan.browser')
    case 'permissions': return t('onboarding.scene.plan.permissions')
    case 'firstRun': return t('onboarding.scene.plan.firstRun')
    case 'finish': return t('onboarding.scene.plan.finish')
    default: return ''
  }
}

const sameStatuses = (a, b) => STEP_IDS.every(id => a[id] === b[id])

// The village behind the onboarding panel. It builds a step's plot when that
// step's status turns `done` between two renders (also late, after the user
// moved on), never just because a step is current, and never for steps that
// were already done when it mounted. The Rebuild control after the last step's
// rampage is a real button outside the aria-hidden scene.
// finished: show the finished village (every plot built, no sunrise, no plan
// tag) and play no builds: the short setup of a later profile.
export default function Scene({ statuses, currentStep, finished = false }) {
  const { t, i18n } = useTranslation()
  const lang = i18n && i18n.language
  const tRef = useRef(t)
  tRef.current = t
  const mountRef = useRef(null)
  const sceneRef = useRef(null)
  const prevRef = useRef(null)
  const stepRef = useRef(null)
  const langRef = useRef(null)
  const finishedRef = useRef(null)
  const [rebuildAt, setRebuildAt] = useState(null)

  useEffect(() => {
    const scene = createScene({
      labels: {
        found: (n, total) => tRef.current('onboarding.scene.found', { n, total }),
        plan: id => planLabel(tRef.current, id),
      },
      onRampageEnd: at => setRebuildAt(at || { left: 0, top: 0 }),
    })
    scene.mount(mountRef.current)
    sceneRef.current = scene
    return () => {
      scene.destroy()
      sceneRef.current = null
      prevRef.current = null
    }
  }, [])

  useEffect(() => {
    const scene = sceneRef.current
    if (!scene) return
    const now = statuses || {}
    const prev = prevRef.current
    /* a parent that re-renders on every keystroke must not make the scene lay itself out again;
       a language switch does refresh its words */
    if (prev && currentStep === stepRef.current && lang === langRef.current && finished === finishedRef.current && sameStatuses(prev, now)) return
    stepRef.current = currentStep
    langRef.current = lang
    finishedRef.current = finished
    if (finished) scene.setState(now, currentStep, { finished: true })
    else scene.setState(now, currentStep)
    if (prev && !finished) STEP_IDS.forEach(id => { if (now[id] === 'done' && prev[id] !== 'done') scene.playBuild(id) })
    prevRef.current = Object.assign({}, now)
    if (currentStep !== 'finish') setRebuildAt(null)
  }, [statuses, currentStep, lang, finished])

  const rebuild = () => {
    if (sceneRef.current) sceneRef.current.rebuild()
    setRebuildAt(null)
  }

  return (
    <div className="onb-scene-host">
      <div className="onb-scene-mount" ref={mountRef} />
      {rebuildAt && (
        <button type="button" className="onb-rebuild" style={{ left: `${rebuildAt.left}px`, top: `${rebuildAt.top}px` }} onClick={rebuild}>
          {t('onboarding.scene.rebuild')}
        </button>
      )}
    </div>
  )
}

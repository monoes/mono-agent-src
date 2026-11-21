import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { BrowserPermissions } from '../../wailsjs/go/main/App'
import { onboarding } from '../../services/onboarding.js'
import { callStep } from '../callStep.js'
import { Problem } from './BrowserGuide.jsx'

// Step "What agents may do in your browser": one machine-wide choice among
// three presets (`browser permissions preset`, ApplyBrowserPermissionPreset).
// The current levels (BrowserPermissions) decide what is preselected: a
// machine whose every kind is still at its default level gets the
// recommended ask; levels equal to a preset's get that preset; anything
// else is custom, nothing is preselected, and Continue keeps it. Continue
// applies the chosen preset, then marks the step.

export const PRESETS = ['ask', 'free', 'off']

// Every kind's level under each preset. Mirrors PresetLevels in
// internal/browserctl/permissions.go (no binding exposes the maps): ask is
// full everywhere, free is full-no-approvals except apply (which can never
// be, so full), off is off everywhere. Kinds: browserctl.Kinds.
const KINDS = ['coder', 'apply', 'org', 'chat', 'other']
const every = (level, over = {}) => ({ ...Object.fromEntries(KINDS.map((k) => [k, level])), ...over })
export const PRESET_LEVELS = {
  ask: every('full'),
  free: every('full-no-approvals', { apply: 'full' }),
  off: every('off'),
}

const sameLevels = (a, b) => {
  const keys = new Set([...Object.keys(a || {}), ...Object.keys(b)])
  return [...keys].every((k) => a?.[k] === b[k])
}

// presetOf: the preset to preselect for a `browser permissions --json`
// answer, or null for custom levels.
export function presetOf(perm) {
  const kinds = perm?.kinds || []
  if (kinds.length > 0 && kinds.every((k) => k.isDefault)) return 'ask'
  return PRESETS.find((p) => sameLevels(perm?.levels, PRESET_LEVELS[p])) || null
}

function Choice({ id }) {
  const { t } = useTranslation()
  switch (id) {
    case 'ask':
      return <><b>{t('onboarding.permissions.ask.title')} <span className="onb-tag ok">{t('onboarding.permissions.recommended')}</span></b><span>{t('onboarding.permissions.ask.desc')}</span></>
    case 'free':
      return <><b>{t('onboarding.permissions.free.title')}</b><span>{t('onboarding.permissions.free.desc')}</span></>
    default:
      return <><b>{t('onboarding.permissions.off.title')}</b><span>{t('onboarding.permissions.off.desc')}</span></>
  }
}

const PeopleIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
    <circle cx="9" cy="8" r="3.5" /><path d="M2.5 20c0-3.6 2.9-6 6.5-6s6.5 2.4 6.5 6M16 4.6a3.5 3.5 0 0 1 0 6.8M18 14.3c2.1.7 3.5 2.8 3.5 5.7" />
  </svg>
)

export default function Permissions({ mark, setCanContinue, setOnContinue }) {
  const { t } = useTranslation()
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState(null)
  const [custom, setCustom] = useState(false)
  const [chosen, setChosen] = useState(null)
  const [saveErr, setSaveErr] = useState(null)
  const chosenRef = useRef(null)
  chosenRef.current = chosen

  const load = useCallback(async () => {
    setLoadErr(null)
    const r = await callStep(() => BrowserPermissions())
    if (!r.ok) { setLoadErr(r); return }
    const p = presetOf(r.value)
    setCustom(!p)
    setChosen((c) => c || p)
    setLoaded(true)
  }, [])
  useEffect(() => { load() }, [load])

  // Continue needs a choice, or custom levels it can keep.
  useEffect(() => { setCanContinue(!!chosen || (loaded && custom)) }, [chosen, loaded, custom, setCanContinue])

  const onContinue = useCallback(async () => {
    setSaveErr(null)
    const preset = chosenRef.current
    if (preset) {
      const r = await callStep(() => onboarding.applyPermissionPreset(preset))
      if (!r.ok) { setSaveErr(r); return false }
    }
    const m = await mark('permissions')
    if (m && m.ok === false) { setSaveErr(m); return false }
    return true
  }, [mark])
  useEffect(() => { setOnContinue(onContinue) }, [onContinue, setOnContinue])

  return (
    <div className="onb-permissions">
      <p className="onb-lead">{t('onboarding.permissions.lead')}</p>
      <Problem r={loadErr} />
      {loadErr && !loadErr.loginRequired && (
        <p><button type="button" className="onb-btn sm" onClick={load}>{t('onboarding.permissions.retry')}</button></p>
      )}
      <div className="onb-rows" role="radiogroup" aria-label={t('onboarding.permissions.choices')}>
        {PRESETS.map((id) => (
          <button key={id} type="button" role="radio" className="onb-opt" aria-checked={chosen === id} onClick={() => setChosen(id)}>
            <div><Choice id={id} /></div>
          </button>
        ))}
      </div>
      {custom && !chosen && <p className="onb-hint" role="status">{t('onboarding.permissions.custom')}</p>}
      <p className="onb-hint">{t('onboarding.permissions.machine')}</p>
      <Problem r={saveErr} />
      <p className="onb-pointer"><PeopleIcon /><span>{t('onboarding.permissions.approvals')}</span></p>
      <p className="onb-muted">{t('onboarding.permissions.settings')}</p>
    </div>
  )
}

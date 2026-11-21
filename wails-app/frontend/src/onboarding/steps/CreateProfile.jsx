// Step 3, Create your profile: a name (with suggestions), one of eight icons
// from the avatar set NewProfileModal offers, an optional folder, and a
// preview of the sidebar entry. "Create profile" calls CreateProfile; Go
// selects the first profile itself, and the step's done status comes from the
// real profile count, so the step only reloads the state (it writes no mark).
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CreateProfile as createProfile, ChooseProfileFolder } from '../../wailsjs/go/main/App'
import { iconUrl } from '../../components/orgdesigner/roleIcons.js'
import { callStep } from '../callStep.js'
import { statusesOf } from '../steps.js'
import './CreateProfile.css'

// Ids from public/org-avatars/agent-avatars.json (the CLI takes such an id).
export const PROFILE_ICONS = ['coder', 'researcher', 'product-manager', 'content-creator', 'studio-producer', 'sales-engineer', 'game-designer', 'data-engineer']

function iconLabel(t, id) {
  switch (id) {
    case 'coder': return t('onboarding.profile.icons.coder')
    case 'researcher': return t('onboarding.profile.icons.researcher')
    case 'product-manager': return t('onboarding.profile.icons.productManager')
    case 'content-creator': return t('onboarding.profile.icons.contentCreator')
    case 'studio-producer': return t('onboarding.profile.icons.studioProducer')
    case 'sales-engineer': return t('onboarding.profile.icons.salesEngineer')
    case 'game-designer': return t('onboarding.profile.icons.gameDesigner')
    default: return t('onboarding.profile.icons.dataEngineer')
  }
}

// The CLI refuses "default" by its look-alike skeleton; this catches the plain
// spelling before the call, and the Go message catches the rest.
const nameProblem = (name) => {
  const n = name.trim()
  if (!n) return 'empty'
  if (n.toLowerCase() === 'default') return 'default'
  return ''
}
const GO_DEFAULT = /cannot be "default"/

export default function CreateProfile({ state, reload, setCanContinue, setOnContinue, setContinueLabel }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [touched, setTouched] = useState(false)
  const [icon, setIcon] = useState(PROFILE_ICONS[0])
  const [rootDir, setRootDir] = useState('')
  const [goError, setGoError] = useState('')
  const [folderError, setFolderError] = useState('')
  const nameRef = useRef(null)
  const iconRefs = useRef([])

  // Coming back (Back, or setup run again) after a profile exists: nothing to
  // create. A second profile made here would not be selected (CreateProfile
  // selects only the first), so the next steps would write to the old one.
  const passThrough = statusesOf(state).profile === 'done'
  const problem = nameProblem(name)

  useEffect(() => {
    setContinueLabel(passThrough ? null : t('onboarding.profile.create'))
    setCanContinue(passThrough || !problem)
  }, [passThrough, problem, t, setCanContinue, setContinueLabel])

  useEffect(() => {
    setOnContinue(async () => {
      if (passThrough) return true
      if (problem) { setTouched(true); nameRef.current?.focus(); return false }
      setGoError('')
      const r = await callStep(() => createProfile(name.trim(), rootDir, icon))
      if (!r.ok) {
        setGoError(r.loginRequired ? t('onboarding.profile.signIn')
          : GO_DEFAULT.test(r.message) ? t('onboarding.profile.errorDefault') : r.message)
        return false
      }
      await reload()
      return true
    })
  })

  const nameError = goError || (touched && problem === 'empty' ? t('onboarding.profile.errorEmpty')
    : touched && problem === 'default' ? t('onboarding.profile.errorDefault') : '')

  const changeName = (value) => {
    setName(value)
    setGoError('')
    if (value.trim()) setTouched(true)
  }
  const suggest = (value) => {
    changeName(value)
    nameRef.current?.focus()
  }

  const pickIcon = (k) => {
    setIcon(PROFILE_ICONS[k])
    iconRefs.current[k]?.focus()
  }
  const iconKey = (e, k) => {
    const n = PROFILE_ICONS.length
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') { e.preventDefault(); pickIcon((k + 1) % n) }
    if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') { e.preventDefault(); pickIcon((k + n - 1) % n) }
  }

  const chooseFolder = async () => {
    setFolderError('')
    const r = await callStep(() => ChooseProfileFolder())
    if (!r.ok) { setFolderError(r.loginRequired ? t('onboarding.profile.signIn') : r.message); return }
    if (typeof r.value === 'string' && r.value) setRootDir(r.value) // '' is a cancelled picker
  }

  const iconIndex = PROFILE_ICONS.indexOf(icon)

  if (passThrough) {
    return (
      <div className="onb-profile">
        <p className="onb-lead">{t('onboarding.profile.lead')}</p>
        <p className="onb-pointer"><span>{t('onboarding.profile.haveOne')}</span></p>
      </div>
    )
  }

  return (
    <div className="onb-profile">
      <p className="onb-lead">{t('onboarding.profile.lead')}</p>

      <div className="onb-field">
        <label htmlFor="onb-profile-name">{t('onboarding.profile.nameLabel')}</label>
        <input
          ref={nameRef}
          id="onb-profile-name"
          className="onb-input"
          type="text"
          autoComplete="off"
          placeholder={t('onboarding.profile.namePlaceholder')}
          value={name}
          onChange={e => changeName(e.target.value)}
          onBlur={() => setTouched(true)}
          aria-invalid={nameError ? 'true' : 'false'}
          aria-required="true"
          aria-describedby={nameError ? 'onb-profile-name-err' : undefined}
        />
        {nameError && <p className="onb-error" id="onb-profile-name-err" role="alert">{nameError}</p>}
        <div className="onb-chips onb-profile-chips" role="group" aria-label={t('onboarding.profile.suggestions')}>
          <button type="button" className="onb-chip" onClick={() => suggest(t('onboarding.profile.suggest.personal'))}>{t('onboarding.profile.suggest.personal')}</button>
          <button type="button" className="onb-chip" onClick={() => suggest(t('onboarding.profile.suggest.work'))}>{t('onboarding.profile.suggest.work')}</button>
          <button type="button" className="onb-chip" onClick={() => suggest(t('onboarding.profile.suggest.client'))}>{t('onboarding.profile.suggest.client')}</button>
        </div>
      </div>

      <div className="onb-field">
        <span className="onb-label" id="onb-profile-icon-label">{t('onboarding.profile.iconLabel')}</span>
        <div className="onb-profile-icons" role="radiogroup" aria-labelledby="onb-profile-icon-label">
          {PROFILE_ICONS.map((id, k) => (
            <button
              key={id}
              ref={el => { iconRefs.current[k] = el }}
              type="button"
              role="radio"
              className="onb-profile-icon"
              aria-checked={k === iconIndex}
              aria-label={iconLabel(t, id)}
              tabIndex={k === iconIndex ? 0 : -1}
              onClick={() => pickIcon(k)}
              onKeyDown={e => iconKey(e, k)}
            >
              <img src={iconUrl(id)} alt="" />
            </button>
          ))}
        </div>
      </div>

      <div className="onb-field">
        <span className="onb-label">{t('onboarding.profile.folderLabel')}</span>
        <div className="onb-profile-folder">
          <code>{rootDir || t('onboarding.profile.folderDefault')}</code>
          <button type="button" className="onb-btn sm" onClick={chooseFolder}>{t('onboarding.profile.change')}</button>
          {rootDir && (
            <button type="button" className="onb-btn sm ghost" onClick={() => setRootDir('')}>{t('onboarding.profile.useDefault')}</button>
          )}
        </div>
        {!rootDir && <p className="onb-hint">{t('onboarding.profile.folderHint')}</p>}
        {folderError && <p className="onb-error" role="alert">{folderError}</p>}
      </div>

      <div className="onb-field onb-profile-last">
        <span className="onb-label">{t('onboarding.profile.previewLabel')}</span>
        <div className="onb-profile-preview" data-testid="profile-preview">
          <img src={iconUrl(icon)} alt="" />
          <div>
            <b>{name.trim() || t('onboarding.profile.previewName')}</b>
            <span>{t('onboarding.profile.previewSub')}</span>
          </div>
        </div>
      </div>
    </div>
  )
}

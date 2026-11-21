// The onboarding's ten steps, in order (the Go side's ids, wails-app/onboarding_derive.go).
// welcome, profile and finish cannot be skipped. The titles and the rail's short
// names go through stepTitle / railLabel so every key is written out in full.

export const STEPS = [
  { id: 'welcome', titleKey: 'onboarding.welcome.title', skippable: false },
  { id: 'system', titleKey: 'onboarding.system.title', skippable: true },
  { id: 'profile', titleKey: 'onboarding.profile.title', skippable: false },
  { id: 'about', titleKey: 'onboarding.about.title', skippable: true },
  { id: 'monomind', titleKey: 'onboarding.monomind.title', skippable: true },
  { id: 'agents', titleKey: 'onboarding.agents.title', skippable: true },
  { id: 'browser', titleKey: 'onboarding.browser.title', skippable: true },
  { id: 'permissions', titleKey: 'onboarding.permissions.title', skippable: true },
  { id: 'firstRun', titleKey: 'onboarding.firstRun.title', skippable: true },
  { id: 'finish', titleKey: 'onboarding.finish.title', skippable: false },
]

export const STEP_IDS = STEPS.map(s => s.id)

// The steps a later profile goes through (mode 'profile'): each of them is the
// selected profile's own.
export const PROFILE_STEPS = ['about', 'monomind', 'browser']

// The steps a mode shows, in order. In mode 'profile' every one is skippable.
export function stepsFor(mode) {
  if (mode !== 'profile') return STEPS
  return STEPS.filter(s => PROFILE_STEPS.includes(s.id)).map(s => ({ ...s, skippable: true }))
}

// { [stepId]: 'done' | 'skipped' | 'todo' } from GetOnboardingState's steps.
export function statusesOf(state) {
  const out = {}
  for (const id of STEP_IDS) out[id] = 'todo'
  for (const s of (state && Array.isArray(state.steps) ? state.steps : [])) {
    if (s && out[s.id] !== undefined && (s.status === 'done' || s.status === 'skipped')) out[s.id] = s.status
  }
  return out
}

// The step's heading, the panel's h1.
export function stepTitle(t, id) {
  switch (id) {
    case 'welcome': return t('onboarding.welcome.title')
    case 'system': return t('onboarding.system.title')
    case 'profile': return t('onboarding.profile.title')
    case 'about': return t('onboarding.about.title')
    case 'monomind': return t('onboarding.monomind.title')
    case 'agents': return t('onboarding.agents.title')
    case 'browser': return t('onboarding.browser.title')
    case 'permissions': return t('onboarding.permissions.title')
    case 'firstRun': return t('onboarding.firstRun.title')
    case 'finish': return t('onboarding.finish.title')
    default: return ''
  }
}

// The step's short name, on the rail and in the eyebrow.
export function railLabel(t, id) {
  switch (id) {
    case 'welcome': return t('onboarding.shell.rail.welcome')
    case 'system': return t('onboarding.shell.rail.system')
    case 'profile': return t('onboarding.shell.rail.profile')
    case 'about': return t('onboarding.shell.rail.about')
    case 'monomind': return t('onboarding.shell.rail.monomind')
    case 'agents': return t('onboarding.shell.rail.agents')
    case 'browser': return t('onboarding.shell.rail.browser')
    case 'permissions': return t('onboarding.shell.rail.permissions')
    case 'firstRun': return t('onboarding.shell.rail.firstRun')
    case 'finish': return t('onboarding.shell.rail.finish')
    default: return ''
  }
}

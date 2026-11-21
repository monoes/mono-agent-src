// What OnboardingGate gives the app. Its own module, so a page inside the app
// can use it without importing the gate (which imports App: a cycle).
//   openOnboarding(stepId?, mode? = 'full' | 'profile'): reads the state again,
//     then shows the onboarding at stepId (default: the state's current step).
//   state: the gate's last GetOnboardingState answer, or null.
//   reload(): reads it again; resolves to callStep's result. On a completed
//     install that read is the fast one, without the per-step facts.
//   loadFull(): reads it with every step's facts (GetOnboardingStateFull), and
//     keeps reading that way; for pages that list what is not done yet.
import { createContext } from 'react'

export const OnboardingContext = createContext({
  openOnboarding: () => Promise.resolve(),
  state: null,
  reload: () => Promise.resolve({ ok: false, loginRequired: false, message: 'no onboarding gate' }),
  loadFull: () => Promise.resolve({ ok: false, loginRequired: false, message: 'no onboarding gate' }),
})

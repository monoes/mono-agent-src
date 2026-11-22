// The Finish-setup card's state read, once per app session. The full read
// (GetOnboardingStateFull) starts several CLI checks, so the card reads it the
// first time it shows and keeps the answer here, outside the card: showing the
// dashboard again, or switching its scope, remounts the card without a read.
// It is read again only after the onboarding was opened from the app (whoever
// opens it calls markSetupStale first; the app remounts when it closes) or on
// the card's own refresh. Nothing is put in the gate's state, so a read never
// re-renders the app.
import { onboarding } from '../../services/onboarding.js'
import { callStep } from '../../onboarding/callStep.js'

let session = { state: null, fresh: false }
let pending = null

// The last answer, and whether it still stands.
export const setupSnapshot = () => session

// One read at a time; resolves to callStep's result.
export function readSetupState() {
  if (!pending) {
    pending = callStep(() => onboarding.stateFull()).then((r) => {
      pending = null
      if (r.ok) session = { state: r.value || null, fresh: true }
      return r
    })
  }
  return pending
}

// The onboarding is about to open: what the card knows may change.
export function markSetupStale() {
  session = { ...session, fresh: false }
}

export function resetSetupSession() { session = { state: null, fresh: false }; pending = null } // tests

// The Finish-setup card's state read, once per app session. The full read
// (GetOnboardingStateFull) starts several CLI checks, so the card reads it the
// first time it shows and keeps the answer here, outside the card: showing the
// dashboard again, or switching its scope, remounts the card without a read.
// The answer stops standing (markSetupStale) when something that changes it
// happened: the onboarding closed (the gate calls markSetupStale), setup was
// restarted, or monomind finished turning on, from any page
// (monomind:initProgress "done"). A card on screen reads again at once
// (onSetupStale); otherwise the card reads the next time it shows, or on its
// own refresh. Nothing is put in the gate's state, so a read
// never re-renders the app.
import { onboarding } from '../../services/onboarding.js'
import { subscribeEvent } from '../../services/api.js'
import { callStep } from '../../onboarding/callStep.js'

let session = { state: null, fresh: false }
let generation = 0 // bumped by markSetupStale: an answer read before it never counts as fresh
let pending = null // { gen, promise }
let watching = false
let storedGen = -1 // the generation of the stored answer: an older read never replaces a newer one
const listeners = new Set()

// The last answer, and whether it still stands.
export const setupSnapshot = () => session

// Some event can change the answer: it no longer stands, and a read already
// under way will not make it stand.
export function markSetupStale() {
  generation++
  session = { ...session, fresh: false }
  for (const fn of [...listeners]) {
    try { fn() } catch (e) { console.error('setup session listener', e) }
  }
}

// fn runs on every markSetupStale (a card on screen reads again); returns the unsubscribe.
export function onSetupStale(fn) {
  listeners.add(fn)
  return () => { listeners.delete(fn) }
}

// Watch for changes made outside the onboarding, once for the session.
function watch() {
  if (watching) return
  watching = true
  try {
    subscribeEvent('monomind:initProgress', (payload) => { if (payload?.kind === 'done') markSetupStale() })
  } catch { watching = false /* no event bus (tests, plain browser): nothing to watch */ }
}

// One read at a time per generation; resolves to callStep's result.
export function readSetupState() {
  watch()
  if (pending && pending.gen === generation) return pending.promise
  const gen = generation
  const promise = callStep(() => onboarding.stateFull()).then((r) => {
    if (pending && pending.promise === promise) pending = null
    if (r.ok && gen >= storedGen) {
      storedGen = gen
      session = { state: r.value || null, fresh: gen === generation }
    }
    return r
  })
  pending = { gen, promise }
  return promise
}

export function resetSetupSession() { session = { state: null, fresh: false }; generation = 0; pending = null; watching = false; storedGen = -1; listeners.clear() } // tests

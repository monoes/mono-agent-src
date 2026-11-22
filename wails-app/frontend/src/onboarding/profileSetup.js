// The request for the short setup of a later profile (onboarding mode
// 'profile'). NewProfileModal records it when it created a profile that Go did
// not select (only the first profile is selected at once); its caller then
// switches to the new profile and reloads the window (Sidebar, through
// SwitchProfile and location.reload). After the reload, OnboardingGate opens
// the short setup if the selected profile is the requested one, and drops the
// request either way. sessionStorage survives the reload and nothing else; a
// request older than PROFILE_SETUP_MAX_AGE_MS is ignored, so a switch that
// failed does not open the setup much later. Storage that throws (a private
// window, blocked site data) only means no short setup.

const KEY = 'monoagent.onboarding.profileSetup'
export const PROFILE_SETUP_MAX_AGE_MS = 2 * 60 * 1000

export function requestProfileSetup(profileId, now = Date.now()) {
  if (!profileId) return
  try { sessionStorage.setItem(KEY, JSON.stringify({ id: String(profileId), at: now })) } catch { /* no storage: no short setup */ }
}

// The requested profile id, or null when there is none or it is too old.
export function pendingProfileSetup(now = Date.now()) {
  try {
    const v = JSON.parse(sessionStorage.getItem(KEY) || 'null')
    if (!v || typeof v.id !== 'string' || typeof v.at !== 'number') return null
    return now - v.at <= PROFILE_SETUP_MAX_AGE_MS && now >= v.at ? v.id : null
  } catch {
    return null
  }
}

export function clearProfileSetup() {
  try { sessionStorage.removeItem(KEY) } catch { /* nothing to clear */ }
}

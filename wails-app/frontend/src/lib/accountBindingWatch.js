// Notices when a Wails binding answers "log in to monoes.me first", so the
// account gate can look again (spec §6.5: "re-checks on every login_required
// answer from any binding"). The generated App.js calls
// window.go.main.App[name] at call time, so wrapping that object once covers
// every binding without touching a caller. Results and rejections pass through
// unchanged; the watch only reads them.
const LOGIN_FIRST = /^Log in to monoes\.me first/
// A login_required answer is a few hundred bytes; never parse a data URL.
const MAX_INSPECTED = 8192
// Bindings that end or change the session: look again once they settle.
const SESSION_CHANGING = new Set(['AccountLogout', 'LibraryLogout'])

// loginRequiredIn: a result that is {login_required: true} (an object, or the
// CLI's JSON text). Plain text that merely starts with the gate's message is
// not enough: a binding can return user-controlled text verbatim.
export function loginRequiredIn(value) {
  if (typeof value === 'string') {
    if (value.length > MAX_INSPECTED) return false
    if (!value.includes('login_required')) return false
    try { return JSON.parse(value)?.login_required === true } catch { return false }
  }
  return !!value && typeof value === 'object' && !Array.isArray(value) && value.login_required === true
}

export function loginRequiredInError(e) {
  const message = typeof e === 'string' ? e : e?.message
  return typeof message === 'string' && LOGIN_FIRST.test(message)
}

// watchBindings wraps every method of window.go.main.App, except the account
// status call the gate itself makes. It returns the function that restores
// them; without the Wails bindings (a test, a browser) it does nothing.
export function watchBindings({ onLoginRequired, onSessionChange }, root = globalThis.window) {
  const app = root?.go?.main?.App
  if (!app || app.__accountWatch) return () => {}
  const originals = {}
  for (const name of Object.keys(app)) {
    const original = app[name]
    if (typeof original !== 'function' || name === 'AccountStatus') continue
    originals[name] = original
    app[name] = function (...args) {
      const out = original.apply(this, args)
      if (!out || typeof out.then !== 'function') return out
      return out.then(
        (value) => {
          if (loginRequiredIn(value)) onLoginRequired()
          else if (SESSION_CHANGING.has(name)) onSessionChange()
          return value
        },
        (e) => {
          if (loginRequiredInError(e)) onLoginRequired()
          throw e
        },
      )
    }
  }
  Object.defineProperty(app, '__accountWatch', { value: true, configurable: true })
  return () => {
    for (const [name, original] of Object.entries(originals)) app[name] = original
    delete app.__accountWatch
  }
}

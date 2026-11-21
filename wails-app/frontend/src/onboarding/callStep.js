// callStep runs one binding call for a step and reads its answer the same way
// whatever style the binding uses:
//   - a thrown error (Go `error`; Wails rejects with its text, a plain string),
//   - a JSON string carrying {"error": "..."} (the CLI-backed bindings),
//   - the sign-in refusal, as JSON {"error", "login_required": true} or as a
//     thrown error whose text starts with the CLI's sign-in line.
// It never rejects: { ok: true, value } or { ok: false, loginRequired, message }.
// A JSON string is parsed into value; any other answer is passed through.

// internal/account/errors.go LoginRequiredMessage starts with this.
const LOGIN_LINE = 'Log in to monoes.me first'

const failure = (message, loginRequired = false) => ({
  ok: false,
  loginRequired: loginRequired || String(message).startsWith(LOGIN_LINE),
  message: String(message),
})

export async function callStep(fn) {
  let raw
  try {
    raw = await fn()
  } catch (e) {
    return failure(e?.message ?? String(e))
  }
  let value = raw
  if (typeof raw === 'string') {
    try { value = JSON.parse(raw) } catch { value = raw }
  }
  if (value && typeof value === 'object' && !Array.isArray(value) && typeof value.error === 'string' && value.error) {
    return failure(value.error, value.login_required === true)
  }
  return { ok: true, value }
}

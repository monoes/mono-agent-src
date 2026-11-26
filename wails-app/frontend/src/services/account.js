// The monoes.me account bindings. status() resolves to the page's one reading of
// `account status`: {status} when the CLI answered a document this app knows,
// else {failure: {cause, message, enforced, enforce_from}}. It never rejects and
// never turns something it could not read into a status: the gate fails closed
// (spec §6.5). enforced says whether this build already enforces; a failure
// that cannot say counts as enforced, and so does a binding that is missing,
// throws or answers a non-object: a page whose Go side cannot be asked shows
// the sign-in screen, never the app (a plain browser or vite dev stubs
// AccountStatus to get past it).
import * as GoApp from '../wailsjs/go/main/App'
import { subscribeEvent } from './api.js'
import i18nOr from '../lib/i18nOr.js'

const STATES = ['ok', 'grace', 'locked']
const CAUSES = ['cli_not_found', 'cli_too_old', 'cli_failed'] // wails-app/app_account.go

const failure = (cause, message = '', more = {}) => ({ failure: { cause, message, enforced: true, ...more } })

// unbound: the binding failed, so nothing is known: locked.
const unbound = (message) => failure('cli_failed', message)

// answerOf reads what AccountStatus returned: a status document (schema 1, a
// state this app knows), the Go side's coded failure, or neither.
export function answerOf(raw) {
  let v = raw
  if (typeof raw === 'string') {
    try { v = JSON.parse(raw) } catch { return failure('cli_failed', i18nOr('ui6PagesMisc.cli_unreadable')) }
  }
  if (v && typeof v === 'object' && v.v === 1 && STATES.includes(v.state)) return { status: v }
  if (v && typeof v.error === 'string') {
    return failure(CAUSES.includes(v.code) ? v.code : 'cli_failed', v.error, { enforced: v.enforced !== false, enforce_from: v.enforce_from })
  }
  return failure('cli_too_old')
}

// run: the CLI's JSON, or {error}; never rejects, like services/library.js.
const run = (call) => Promise.resolve().then(call).then(r => (typeof r === 'string' ? JSON.parse(r) : r))
  .catch(e => ({ error: e?.message || String(e) }))

export const account = {
  status: () => Promise.resolve().then(() => GoApp.AccountStatus())
    .then(raw => (typeof raw === 'string' || (raw && typeof raw === 'object') ? answerOf(raw) : unbound(i18nOr('ui6PagesMisc.no_answer'))))
    .catch(e => unbound(e?.message || String(e))),
  login: () => run(() => GoApp.AccountLogin()),
  cancelLogin: () => run(() => GoApp.AccountLoginCancel()),
  sendCode: (email) => run(() => GoApp.AccountLoginEmailSend(email)),
  verifyCode: (email, code) => run(() => GoApp.AccountLoginEmailVerify(email, code)),
  logout: () => run(() => GoApp.AccountLogout()),
}

// "account:login" carries the CLI's login progress ({kind:"url", url} first).
export const onAccountLogin = (callback) => subscribeEvent('account:login', callback)

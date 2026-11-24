import { describe, it, expect, vi } from 'vitest'
import { loginRequiredIn, loginRequiredInError, watchBindings } from './accountBindingWatch.js'

const LOGIN_REQUIRED = JSON.stringify({
  error: 'Log in to monoes.me first: monoagentcli account login', code: 'auth_or_connection',
  login_required: true, account: { state: 'locked', reason: 'not_logged_in' },
})
const GATE_MESSAGE = 'Log in to monoes.me first: monoagentcli account login'
const appWith = (methods) => ({ go: { main: { App: methods } } })
const callbacks = () => ({ onLoginRequired: vi.fn(), onSessionChange: vi.fn() })

describe('what counts as a login_required answer', () => {
  it('is the CLI\'s JSON, an object with the flag, or the gate\'s own message', () => {
    for (const v of [LOGIN_REQUIRED, { login_required: true }]) expect(loginRequiredIn(v)).toBe(true)
    for (const e of [new Error(GATE_MESSAGE), GATE_MESSAGE]) expect(loginRequiredInError(e)).toBe(true)
  })

  it('is not a payload that merely mentions it, nor an ordinary answer, nor a big string', () => {
    const quiet = [JSON.stringify({ rows: [{ error: 'login_required: Log in to monoes.me first' }] }), JSON.stringify({ login_required: false }),
      '{"login_required": broken', ['login_required'], GATE_MESSAGE, `${GATE_MESSAGE} (quoted from a document)`, null, 42, 'x'.repeat(100000) + 'login_required']
    for (const v of quiet) expect(loginRequiredIn(v)).toBe(false)
    for (const e of [new Error('workflow failed: login_required: Log in to monoes.me first'), undefined]) expect(loginRequiredInError(e)).toBe(false)
  })
})

describe('watchBindings', () => {
  it('reports a login_required answer from any binding, and a rejection with the gate\'s message, and passes both through', async () => {
    const err = new Error(GATE_MESSAGE)
    const win = appWith({
      ListWorkflows: vi.fn(() => Promise.resolve(LOGIN_REQUIRED)), GetPeople: vi.fn(() => Promise.resolve('[]')),
      GetRecentExecutions: vi.fn(() => Promise.reject(err)), Other: vi.fn(() => Promise.reject(new Error('disk full'))),
    })
    const cb = callbacks()
    watchBindings(cb, win)
    const App = win.go.main.App

    expect(await App.GetPeople(1, 2)).toBe('[]')
    expect(cb.onLoginRequired).not.toHaveBeenCalled()
    expect(await App.ListWorkflows()).toBe(LOGIN_REQUIRED)
    expect(cb.onLoginRequired).toHaveBeenCalledTimes(1)
    await expect(App.GetRecentExecutions()).rejects.toBe(err)
    expect(cb.onLoginRequired).toHaveBeenCalledTimes(2)
    await expect(App.Other()).rejects.toThrow('disk full')
    expect(cb.onLoginRequired).toHaveBeenCalledTimes(2)
  })

  it('looks again after a logout settles, and never wraps the gate\'s own status call', async () => {
    const status = vi.fn(() => Promise.resolve(LOGIN_REQUIRED))
    const win = appWith({ AccountStatus: status, LibraryLogout: vi.fn(() => Promise.resolve('{"logged_out":true}')) })
    const cb = callbacks()
    watchBindings(cb, win)
    expect(win.go.main.App.AccountStatus).toBe(status)
    await win.go.main.App.AccountStatus()
    expect(cb.onLoginRequired).not.toHaveBeenCalled()
    await win.go.main.App.LibraryLogout()
    expect(cb.onSessionChange).toHaveBeenCalledTimes(1)
  })

  it('wraps once, the returned function puts every binding back, and without bindings it does nothing', async () => {
    const original = vi.fn(() => Promise.resolve(LOGIN_REQUIRED))
    const win = appWith({ ListWorkflows: original })
    const cb = callbacks()
    const stop = watchBindings(cb, win)
    const wrapped = win.go.main.App.ListWorkflows
    expect(wrapped).not.toBe(original)
    watchBindings(callbacks(), win) // a second start (StrictMode) wraps nothing again
    expect(win.go.main.App.ListWorkflows).toBe(wrapped)

    stop()
    expect(win.go.main.App.ListWorkflows).toBe(original)
    await win.go.main.App.ListWorkflows()
    expect(cb.onLoginRequired).not.toHaveBeenCalled()
    expect(() => watchBindings(callbacks(), {})()).not.toThrow()
  })
})

import { describe, it, expect } from 'vitest'
import { callStep } from './callStep.js'

const LOGIN = 'Log in to monoes.me first: monoagentcli account login'

describe('callStep', () => {
  it('maps the login-required JSON to loginRequired: true', async () => {
    const raw = JSON.stringify({ error: LOGIN, login_required: true, code: 'auth_or_connection' })
    expect(await callStep(() => Promise.resolve(raw))).toEqual({ ok: false, loginRequired: true, message: LOGIN })
  })

  it('maps a JSON error string and a thrown error to ok: false with the message', async () => {
    expect(await callStep(() => Promise.resolve('{"error":"no such preset"}')))
      .toEqual({ ok: false, loginRequired: false, message: 'no such preset' })
    expect(await callStep(() => Promise.reject(new Error('disk full'))))
      .toEqual({ ok: false, loginRequired: false, message: 'disk full' })
    // Wails rejects with the Go error's text, a plain string.
    expect(await callStep(() => Promise.reject('onboarding step "x" cannot be marked')))
      .toEqual({ ok: false, loginRequired: false, message: 'onboarding step "x" cannot be marked' })
    // A binding that is missing throws before it returns a promise.
    expect(await callStep(() => { throw new TypeError('not a function') }))
      .toEqual({ ok: false, loginRequired: false, message: 'not a function' })
  })

  it('maps a thrown sign-in refusal to loginRequired: true', async () => {
    expect(await callStep(() => Promise.reject(`${LOGIN}\nYour sign-in expired.`)))
      .toEqual({ ok: false, loginRequired: true, message: `${LOGIN}\nYour sign-in expired.` })
  })

  it('passes a value through: objects, arrays, JSON strings and nothing', async () => {
    const state = { completed: false, currentStep: 'welcome', steps: [] }
    expect(await callStep(() => Promise.resolve(state))).toEqual({ ok: true, value: state })
    expect(await callStep(() => Promise.resolve([{ id: 'chrome' }]))).toEqual({ ok: true, value: [{ id: 'chrome' }] })
    expect(await callStep(() => Promise.resolve('{"levels":{}}'))).toEqual({ ok: true, value: { levels: {} } })
    expect(await callStep(() => Promise.resolve(undefined))).toEqual({ ok: true, value: undefined })
    expect(await callStep(() => Promise.resolve('plain text'))).toEqual({ ok: true, value: 'plain text' })
  })
})

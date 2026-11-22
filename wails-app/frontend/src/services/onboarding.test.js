// The onboarding wrappers hand the binding's own answer to callStep: a binding
// that rejects (DetectChromiumBrowsers signed out, a Go error) comes back as a
// failure, the sign-in refusal as loginRequired.
import { describe, it, expect, vi, beforeEach } from 'vitest'

const go = vi.hoisted(() => ({ DetectChromiumBrowsers: vi.fn(), OpenChromiumBrowser: vi.fn() }))
vi.mock('../wailsjs/go/main/App', () => go)

import { onboarding } from './onboarding.js'
import { callStep } from '../onboarding/callStep.js'

const LOGIN = 'Log in to monoes.me first: monoagentcli account login'

beforeEach(() => { go.DetectChromiumBrowsers.mockReset(); go.OpenChromiumBrowser.mockReset() })

describe('services/onboarding through callStep', () => {
  it('passes the detected browsers through', async () => {
    go.DetectChromiumBrowsers.mockResolvedValue([{ id: 'chrome', installed: true }])
    expect(await callStep(() => onboarding.detectBrowsers())).toEqual({ ok: true, value: [{ id: 'chrome', installed: true }] })
  })

  it('a DetectChromiumBrowsers rejection while signed out is loginRequired', async () => {
    go.DetectChromiumBrowsers.mockRejectedValue(LOGIN)
    expect(await callStep(() => onboarding.detectBrowsers())).toEqual({ ok: false, loginRequired: true, message: LOGIN })
  })

  it('any other rejection is a plain failure', async () => {
    go.DetectChromiumBrowsers.mockRejectedValue('listing browsers: permission denied')
    expect(await callStep(() => onboarding.detectBrowsers())).toEqual({ ok: false, loginRequired: false, message: 'listing browsers: permission denied' })
    go.OpenChromiumBrowser.mockRejectedValue(new Error('no browser "x"'))
    expect(await callStep(() => onboarding.openBrowser('x'))).toEqual({ ok: false, loginRequired: false, message: 'no browser "x"' })
  })
})

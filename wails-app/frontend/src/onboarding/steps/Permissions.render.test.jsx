// @vitest-environment jsdom
// Step "What agents may do in your browser": one of three presets
// (ApplyBrowserPermissionPreset), preselected from the current levels
// (BrowserPermissions); custom levels are kept when nothing is chosen.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor, act } from '@testing-library/react'
import i18n from '../../i18n.js'

vi.mock('../../wailsjs/go/main/App', () => ({
  BrowserPermissions: vi.fn(),
  ApplyBrowserPermissionPreset: vi.fn(),
}))

import * as Go from '../../wailsjs/go/main/App'
import Permissions from './Permissions.jsx'

const KINDS = ['coder', 'apply', 'org', 'chat', 'other']
const DEFAULTS = { coder: 'dev', apply: 'ask', org: 'dev', chat: 'full', other: 'dev' }
// The answer of `browser permissions --json` for the given levels.
const doc = (levels) => JSON.stringify({
  levels,
  sites: [],
  kinds: KINDS.map((k) => ({ kind: k, label: k, level: levels[k], isDefault: levels[k] === DEFAULTS[k] })),
  catalog: [],
  you: { kind: 'human', level: 'unrestricted' },
})

let props
beforeEach(async () => {
  await i18n.changeLanguage('en')
  Go.BrowserPermissions.mockResolvedValue(doc(DEFAULTS))
  Go.ApplyBrowserPermissionPreset.mockImplementation((p) => Promise.resolve(doc(Object.fromEntries(KINDS.map((k) => [k, p === 'off' ? 'off' : 'full'])))))
  props = {
    state: null, profileId: 'p1', mode: 'full',
    reload: vi.fn(), skip: vi.fn(), complete: vi.fn(),
    mark: vi.fn(() => Promise.resolve({ ok: true })),
    onDone: vi.fn(), onFinish: vi.fn(), goTo: vi.fn(),
    setCanContinue: vi.fn(), setOnContinue: vi.fn(), setContinueLabel: vi.fn(),
  }
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

const show = () => render(<Permissions {...props} />)
const option = (name) => screen.getByRole('radio', { name: new RegExp(name) })
// Runs the handler the step registered last, as Continue would.
const runContinue = async () => {
  let out
  await act(async () => { out = await props.setOnContinue.mock.calls.at(-1)[0]() })
  return out
}

describe('What agents may do in your browser', () => {
  it('preselects ask on a machine with the default levels', async () => {
    show()
    await waitFor(() => expect(option('Ask before anything is submitted')).toHaveAttribute('aria-checked', 'true'))
    expect(option('Let agents work freely')).toHaveAttribute('aria-checked', 'false')
    expect(option('Keep agents out of my browser')).toHaveAttribute('aria-checked', 'false')
    expect(option('Ask before anything is submitted')).toHaveTextContent('Recommended')
    expect(screen.getByText('This applies to every profile on this machine.')).toBeInTheDocument()
    expect(screen.getByText('When an agent needs your OK, it waits in Human-in-Loop until you approve or reject.')).toBeInTheDocument()
    expect(screen.getByText('You can set this per kind of agent later in Settings, under Browser permissions.')).toBeInTheDocument()
    expect(screen.queryByText('You have custom settings. Continue keeps them.')).not.toBeInTheDocument()
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
  })

  it('preselects the preset the current levels match', async () => {
    Go.BrowserPermissions.mockResolvedValue(doc({ coder: 'full-no-approvals', apply: 'full', org: 'full-no-approvals', chat: 'full-no-approvals', other: 'full-no-approvals' }))
    show()
    await waitFor(() => expect(option('Let agents work freely')).toHaveAttribute('aria-checked', 'true'))
  })

  it('Continue applies the chosen preset and marks the step', async () => {
    show()
    await waitFor(() => expect(option('Ask before anything is submitted')).toHaveAttribute('aria-checked', 'true'))
    fireEvent.click(option('Keep agents out of my browser'))
    expect(option('Keep agents out of my browser')).toHaveAttribute('aria-checked', 'true')
    const out = await runContinue()
    expect(out).not.toBe(false)
    expect(Go.ApplyBrowserPermissionPreset).toHaveBeenCalledWith('off')
    expect(Go.ApplyBrowserPermissionPreset).toHaveBeenCalledTimes(1)
    expect(props.mark).toHaveBeenCalledWith('permissions')
    expect(Go.ApplyBrowserPermissionPreset.mock.invocationCallOrder[0]).toBeLessThan(props.mark.mock.invocationCallOrder[0])
  })

  it('custom levels are kept when nothing is chosen', async () => {
    Go.BrowserPermissions.mockResolvedValue(doc({ ...DEFAULTS, coder: 'full' }))
    show()
    expect(await screen.findByText('You have custom settings. Continue keeps them.')).toBeInTheDocument()
    for (const r of screen.getAllByRole('radio')) expect(r).toHaveAttribute('aria-checked', 'false')
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
    const out = await runContinue()
    expect(out).not.toBe(false)
    expect(Go.ApplyBrowserPermissionPreset).not.toHaveBeenCalled()
    expect(props.mark).toHaveBeenCalledWith('permissions')
  })

  it('a failed save shows the error and does not mark the step', async () => {
    Go.ApplyBrowserPermissionPreset.mockResolvedValue(JSON.stringify({ error: 'database is locked', code: 'db' }))
    show()
    await waitFor(() => expect(option('Ask before anything is submitted')).toHaveAttribute('aria-checked', 'true'))
    const out = await runContinue()
    expect(out).toBe(false)
    expect(Go.ApplyBrowserPermissionPreset).toHaveBeenCalledWith('ask')
    expect(await screen.findByText('database is locked')).toBeInTheDocument()
    expect(props.mark).not.toHaveBeenCalled()
  })

  it('a sign-in refusal while reading shows the sign-in message and still lets a preset be chosen', async () => {
    Go.BrowserPermissions.mockResolvedValue(JSON.stringify({ error: 'Log in to monoes.me first', login_required: true }))
    show()
    expect(await screen.findByText(i18n.t('onboarding.shell.signIn'))).toBeInTheDocument()
    expect(props.setCanContinue).not.toHaveBeenCalledWith(true)
    fireEvent.click(option('Let agents work freely'))
    await waitFor(() => expect(props.setCanContinue).toHaveBeenLastCalledWith(true))
  })
})

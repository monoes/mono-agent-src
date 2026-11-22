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
const doc = (levels, presets) => JSON.stringify({
  levels,
  ...(presets ? { presets } : {}),
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
    expect(option('Keep my current settings')).toHaveAttribute('aria-checked', 'true')
    for (const name of ['Ask before anything is submitted', 'Let agents work freely', 'Keep agents out of my browser']) {
      expect(option(name)).toHaveAttribute('aria-checked', 'false')
    }
    expect(props.setCanContinue).toHaveBeenLastCalledWith(true)
    // a preset clicked by mistake can be taken back
    fireEvent.click(option('Keep agents out of my browser'))
    expect(screen.queryByText('You have custom settings. Continue keeps them.')).not.toBeInTheDocument()
    fireEvent.click(option('Keep my current settings'))
    expect(screen.getByText('You have custom settings. Continue keeps them.')).toBeInTheDocument()
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

  it('says what the recommended preset widens only when the defaults picked it', async () => {
    const WIDENS = 'Today most agents are limited to local and dev pages. This choice lets them open any site; submits still wait for you.'
    show()
    expect(await screen.findByText(WIDENS)).toBeInTheDocument()
    fireEvent.click(option('Let agents work freely'))
    expect(screen.queryByText(WIDENS)).not.toBeInTheDocument()
    fireEvent.click(option('Ask before anything is submitted'))
    expect(screen.getByText(WIDENS)).toBeInTheDocument()
    cleanup()
    // a machine already at ask: nothing changes, so no such line
    Go.BrowserPermissions.mockResolvedValue(doc(Object.fromEntries(KINDS.map((k) => [k, 'full']))))
    show()
    await waitFor(() => expect(option('Ask before anything is submitted')).toHaveAttribute('aria-checked', 'true'))
    expect(screen.queryByText(WIDENS)).not.toBeInTheDocument()
  })

  it('reads the preset maps from the answer when it carries them', async () => {
    // levels that equal no mirrored preset, but the answer's own `off`
    const levels = Object.fromEntries(KINDS.map((k) => [k, 'ask']))
    Go.BrowserPermissions.mockResolvedValue(doc(levels, {
      ask: Object.fromEntries(KINDS.map((k) => [k, 'full'])),
      free: Object.fromEntries(KINDS.map((k) => [k, 'full'])),
      off: levels,
    }))
    show()
    await waitFor(() => expect(option('Keep agents out of my browser')).toHaveAttribute('aria-checked', 'true'))
    expect(screen.queryByText('You have custom settings. Continue keeps them.')).not.toBeInTheDocument()
    cleanup()
    // the same levels without `presets` fall back to the mirror: custom
    Go.BrowserPermissions.mockResolvedValue(doc(levels))
    show()
    expect(await screen.findByText('You have custom settings. Continue keeps them.')).toBeInTheDocument()
  })

  it('Try again reads the settings again after a failed read', async () => {
    Go.BrowserPermissions.mockRejectedValueOnce('browser permissions: database is locked')
    show()
    expect(await screen.findByText('browser permissions: database is locked')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(option('Ask before anything is submitted')).toHaveAttribute('aria-checked', 'true'))
    expect(Go.BrowserPermissions).toHaveBeenCalledTimes(2)
    expect(screen.queryByText('browser permissions: database is locked')).not.toBeInTheDocument()
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

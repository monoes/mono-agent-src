// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within, act } from '@testing-library/react'

const go = vi.hoisted(() => ({
  AccountStatus: vi.fn(), AccountLogin: vi.fn(), AccountLoginCancel: vi.fn(), AccountLogout: vi.fn(),
  AccountLoginEmailSend: vi.fn(), AccountLoginEmailVerify: vi.fn(), AppSelfUpdate: vi.fn(), QuitApp: vi.fn(),
}))
vi.mock('../../wailsjs/go/main/App', () => go)

import i18n from '../../i18n.js'
import { createAccountGate } from '../../lib/accountGate.js'
import { account } from '../../services/account.js'
import AccountShell from './AccountShell.jsx'

const j = (v) => Promise.resolve(JSON.stringify(v))
const DATE = '2026-10-26T00:00:00Z'
const doc = (over = {}) => ({ v: 1, state: 'ok', reason: '', plan: 'free', enforced: true, enforce_from: DATE, ...over })
const LOCKED = doc({ state: 'locked', reason: 'not_logged_in' })
const LOGIN_REQUIRED = { error: 'Log in to monoes.me first: monoagentcli account login', code: 'auth_or_connection', login_required: true }
const answer = (v) => go.AccountStatus.mockImplementation(() => j(v))
const WHEN = { dateStyle: 'medium', timeStyle: 'short' }

// The app, standing in for the real shell: it wears the banners it is given.
const shell = (banners) => <div data-testid="shell">{banners}<span>the app</span></div>

function setup(now = () => Date.now()) {
  const gate = createAccountGate({ status: account.status, now })
  return render(<AccountShell gate={gate} renderShell={shell} />)
}
const gateRegion = (name) => screen.findByRole('region', { name })
const signIn = (scope) => scope.getByRole('button', { name: /Log in to monoes/ })

beforeEach(async () => {
  await i18n.changeLanguage('en')
  answer(doc())
  go.AccountLogin.mockImplementation(() => j({ v: 1, state: 'ok' }))
})
afterEach(() => { cleanup(); vi.clearAllMocks(); vi.useRealTimers() })

describe('the account gate over the app', () => {
  it('shows a splash until the first answer, then the app with no banner for a signed-in machine', async () => {
    let reply
    go.AccountStatus.mockImplementation(() => new Promise(resolve => { reply = resolve }))
    setup()
    expect(screen.getByRole('status')).toHaveTextContent('Checking your monoes.me account…')
    expect(screen.queryByTestId('shell')).not.toBeInTheDocument()
    await waitFor(() => expect(go.AccountStatus).toHaveBeenCalledTimes(1))
    await act(async () => { reply(JSON.stringify(doc())) })
    const app = await screen.findByTestId('shell')
    expect(within(app).queryByRole('status')).not.toBeInTheDocument()
    expect(screen.queryByRole('region')).not.toBeInTheDocument()
  })

  it('renders the gate instead of the app when locked, with the sign-in controls', async () => {
    answer(LOCKED)
    setup()
    const region = within(await gateRegion('Sign in to MonoAgent'))
    expect(signIn(region)).toBeInTheDocument()
    expect(region.getByText('Use an email code instead')).toBeInTheDocument()
    expect(screen.queryByTestId('shell')).not.toBeInTheDocument()
  })

  it('says why for each reason, and offers the update where signing in again would not help', async () => {
    const cases = { expired: ['Your sign-in has expired', true], refused: ['monoes.me ended this sign-in', true],
      clock_skew: ["This computer's clock looks wrong", true], key_unknown: ['This version cannot verify your sign-in', false],
      unconfirmed: ['This computer stopped using its saved sign-in', true] }
    for (const [reason, [title, canSignIn]] of Object.entries(cases)) {
      answer(doc({ state: 'locked', reason }))
      const { unmount } = setup()
      const region = within(await gateRegion(title))
      expect(!!region.queryByRole('button', { name: /Log in to monoes/ }), reason).toBe(canSignIn)
      expect(!!region.queryByRole('button', { name: 'Update MonoAgent' }), reason).toBe(!canSignIn)
      unmount()
    }
  })

  it('does not lock before the enforcement date: a warn banner with the date and a way to sign in now', async () => {
    answer(doc({ state: 'locked', enforced: false }))
    setup()
    const app = within(await screen.findByTestId('shell'))
    const date = new Date(DATE).toLocaleDateString('en', { year: 'numeric', month: 'long', day: 'numeric' })
    expect(app.getByRole('status')).toHaveTextContent(`A monoes.me sign-in will be required from ${date}.`)

    answer(doc())
    fireEvent.click(app.getByRole('button', { name: 'Sign in' }))
    fireEvent.click(signIn(within(await screen.findByRole('dialog'))))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByTestId('shell')).toBeInTheDocument()
    expect(screen.queryByText(/will be required from/)).not.toBeInTheDocument()
  })

  it('says nothing, and never locks, for a build with no date (dormant), whatever the state', async () => {
    for (const state of ['locked', 'grace']) {
      answer(doc({ state, reason: 'unreachable', enforced: false, enforce_from: undefined, grace_until: '2026-10-06T21:20:00Z' }))
      const { unmount } = setup()
      expect(within(await screen.findByTestId('shell')).queryByRole('status')).not.toBeInTheDocument()
      unmount()
    }
  })

  it('wears a dismissible grace banner with the time left, and names a key store that cannot be opened', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-06T08:00:00Z'))
    const graceUntil = '2026-10-06T21:20:00Z' // 13 h 20 min away
    answer(doc({ state: 'grace', reason: 'unreachable', grace_until: graceUntil }))
    const first = setup()
    const banner = await screen.findByRole('status')
    const when = new Date(graceUntil).toLocaleString('en', WHEN)
    expect(banner).toHaveTextContent(`monoes.me is unreachable. Your sign-in keeps working offline until ${when} (13 hours left).`)
    fireEvent.click(within(banner).getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.getByTestId('shell')).toBeInTheDocument()
    first.unmount()

    answer(doc({ state: 'grace', reason: 'keyring_unavailable', grace_until: '2026-10-06T08:41:00Z' }))
    setup()
    expect(await screen.findByRole('status')).toHaveTextContent(/cannot open the key store.*\(41 minutes left\)/)
  })

  // A24: this computer dropped its saved sign-in because a refresh may have reached monoes.me without its answer
  // arriving. monoes.me is not the problem and "offline" is not the fix: the banner says that the sign-in cannot
  // be renewed, until when it works, and offers to sign in again, which makes the machine whole.
  it('says that this computer can no longer renew its sign-in, and offers to sign in again', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-06T08:00:00Z'))
    const graceUntil = '2026-10-06T21:20:00Z' // 13 h 20 min away
    answer(doc({ state: 'grace', reason: 'unconfirmed', grace_until: graceUntil }))
    setup()
    const banner = await screen.findByRole('status')
    const when = new Date(graceUntil).toLocaleString('en', WHEN)
    expect(banner).toHaveTextContent(`monoes.me may have received a refresh whose answer never arrived, so this computer can no longer renew its sign-in. It works until ${when} (13 hours left); sign in again before then.`)
    expect(banner).not.toHaveTextContent('unreachable')

    answer(doc())
    fireEvent.click(within(banner).getByRole('button', { name: 'Sign in' }))
    fireEvent.click(signIn(within(await screen.findByRole('dialog'))))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByTestId('shell')).toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  // A newer monoagentcli may report a reason this app does not know: the gate and the banner fall back to
  // generic words and never to a blank or a raw key, and a locked machine can always sign in.
  it('uses generic words for a reason this app does not know', async () => {
    answer(doc({ state: 'locked', reason: 'from_the_future' }))
    const first = setup()
    const region = within(await gateRegion('Your sign-in could not be verified'))
    expect(signIn(region)).toBeInTheDocument()
    first.unmount()

    answer(doc({ state: 'grace', reason: 'from_the_future', grace_until: '2026-10-06T21:20:00Z' }))
    setup()
    expect(await screen.findByRole('status')).toHaveTextContent('monoes.me is unreachable.')
  })
})

describe('returning to the gate', () => {
  afterEach(() => { delete window.go; delete window.runtime })

  it('goes back to the gate when any binding answers login_required', async () => {
    window.go = { main: { App: { ListWorkflows: vi.fn(() => Promise.resolve(JSON.stringify(LOGIN_REQUIRED))) } } }
    window.runtime = { EventsOnMultiple: () => () => {} } // the sign-in button subscribes to events
    let t = 1_000_000
    setup(() => t)
    await screen.findByTestId('shell')

    t += 10_000
    answer(doc({ state: 'locked', reason: 'refused' }))
    await act(async () => { await window.go.main.App.ListWorkflows() })
    expect(await gateRegion('monoes.me ended this sign-in')).toBeInTheDocument()
    expect(screen.queryByTestId('shell')).not.toBeInTheDocument()
  })

  it('goes back to the gate when the window gets the focus after the session was refused', async () => {
    let t = 1_000_000
    setup(() => t)
    await screen.findByTestId('shell')
    t += 10_000
    answer(LOCKED)
    await act(async () => { window.dispatchEvent(new Event('focus')) })
    expect(await gateRegion('Sign in to MonoAgent')).toBeInTheDocument()
  })

  it('returns to the app when the person signs in on the gate', async () => {
    answer(LOCKED)
    setup()
    const region = within(await gateRegion('Sign in to MonoAgent'))
    answer(doc())
    fireEvent.click(signIn(region))
    expect(await screen.findByTestId('shell')).toBeInTheDocument()
    expect(go.AccountLogin).toHaveBeenCalledTimes(1)
  })
})

describe('failing closed', () => {
  it('locks, with the cause, when `account status` cannot be run, and offers retry only', async () => {
    answer({ error: 'monoagentcli binary not found', code: 'cli_not_found' })
    setup()
    const region = within(await gateRegion('MonoAgent cannot find its command-line tool'))
    // The raw message (it may carry paths or stderr) sits behind a collapsed toggle.
    expect(region.queryByText('monoagentcli binary not found')).not.toBeInTheDocument()
    const toggle = region.getByRole('button', { name: 'Technical details' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(region.getByText('monoagentcli binary not found')).toBeInTheDocument()
    fireEvent.click(toggle)
    expect(region.queryByText('monoagentcli binary not found')).not.toBeInTheDocument()
    expect(region.queryByRole('button', { name: 'Update MonoAgent' })).not.toBeInTheDocument()
    answer(doc())
    fireEvent.click(region.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByTestId('shell')).toBeInTheDocument()
  })

  it('locks on a CLI that answers something this app does not know, and offers the update', async () => {
    answer({ v: 2, state: 'ok' })
    setup()
    const region = within(await gateRegion("MonoAgent's command-line tool is out of date"))
    go.AppSelfUpdate.mockImplementation(() => Promise.resolve({ success: false, error: 'checksum mismatch' }))
    fireEvent.click(region.getByRole('button', { name: 'Update MonoAgent' }))
    expect(await region.findByRole('alert')).toHaveTextContent('The update failed: checksum mismatch')
    go.AppSelfUpdate.mockImplementation(() => Promise.resolve({ success: true, new_version: 'v9.9.9' }))
    fireEvent.click(region.getByRole('button', { name: 'Update MonoAgent' }))
    expect(await region.findByText('Updated. MonoAgent restarts to finish.')).toBeInTheDocument()
    expect(go.AppSelfUpdate).toHaveBeenCalledTimes(2)
  })

  it('shows the notice of an update that did not restart the daemon, and quits only when asked', async () => {
    answer({ v: 2, state: 'ok' })
    setup()
    const region = within(await gateRegion("MonoAgent's command-line tool is out of date"))
    go.AppSelfUpdate.mockImplementation(() => Promise.resolve({ success: true, new_version: 'v9.9.9', notice: 'daemon is down\nThe update is installed; restart the app to use it.' }))
    fireEvent.click(region.getByRole('button', { name: 'Update MonoAgent' }))
    expect(await region.findByTestId('update-notice')).toHaveTextContent('daemon is down')
    expect(go.QuitApp).not.toHaveBeenCalled()
    fireEvent.click(region.getByTestId('update-notice-restart'))
    expect(go.QuitApp).toHaveBeenCalledTimes(1)
  })

  it('does not lock on a failure before this build enforces: it warns with the date', async () => {
    answer({ error: 'old CLI', code: 'cli_too_old', enforced: false, enforce_from: DATE })
    setup()
    expect(within(await screen.findByTestId('shell')).getByRole('status')).toHaveTextContent('A monoes.me sign-in will be required from')
  })

  it('offers the update on any locked screen once the app has found a release', async () => {
    let found
    window.runtime = { EventsOnMultiple: (name, cb) => { if (name === 'update:available') found = cb; return () => {} } }
    window.go = {}
    answer(LOCKED)
    setup()
    const region = within(await gateRegion('Sign in to MonoAgent'))
    expect(region.queryByRole('button', { name: 'Update MonoAgent' })).not.toBeInTheDocument()
    await act(async () => { found({ update_available: true, latest_version: 'v9.9.9' }) })
    expect(region.getByText('Version v9.9.9 is available.')).toBeInTheDocument()
    expect(region.getByRole('button', { name: 'Update MonoAgent' })).toBeInTheDocument()
    delete window.go; delete window.runtime
  })

  it('offers no update for a missing CLI, whatever release is known: the update installs through that CLI', async () => {
    let found
    window.runtime = { EventsOnMultiple: (name, cb) => { if (name === 'update:available') found = cb; return () => {} } }
    window.go = {}
    answer({ error: 'monoagentcli binary not found', code: 'cli_not_found' })
    setup()
    const region = within(await gateRegion('MonoAgent cannot find its command-line tool'))
    await act(async () => { found({ update_available: true, latest_version: 'v9.9.9' }) })
    expect(region.queryByText('Version v9.9.9 is available.')).not.toBeInTheDocument()
    expect(region.queryByRole('button', { name: 'Update MonoAgent' })).not.toBeInTheDocument()
    expect(region.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
    delete window.go; delete window.runtime
  })

  it('locks when the status binding itself fails: a page with no Go side to ask shows the gate, not the app', async () => {
    go.AccountStatus.mockImplementation(() => Promise.reject(new Error('binding missing')))
    setup()
    expect(await screen.findByRole('region')).toBeInTheDocument()
    expect(screen.queryByTestId('shell')).not.toBeInTheDocument()
  })
})

describe('in Spanish', () => {
  beforeEach(async () => { await i18n.changeLanguage('es') })

  it('words the gate, the failure and both banners', async () => {
    answer(LOCKED)
    const a = setup()
    expect(within(await gateRegion('Inicia sesión en MonoAgent')).getByRole('button', { name: /Iniciar sesión en monoes/ })).toBeInTheDocument()
    a.unmount()

    answer({ v: 2, state: 'ok' })
    const b = setup()
    expect(within(await gateRegion('La herramienta de línea de comandos de MonoAgent está desactualizada')).getByRole('button', { name: 'Actualizar MonoAgent' })).toBeInTheDocument()
    b.unmount()

    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-06T08:00:00Z'))
    answer(doc({ state: 'grace', reason: 'unreachable', grace_until: '2026-10-06T09:00:00Z' }))
    const c = setup()
    expect(await screen.findByRole('status')).toHaveTextContent(/No se puede conectar con monoes\.me\. Tu sesión sigue funcionando sin conexión hasta el .*\(queda 1 hora\)/)
    c.unmount()

    answer(doc({ state: 'locked', enforced: false, enforce_from: '2026-10-26T00:00:00Z' }))
    setup()
    expect(await screen.findByRole('status')).toHaveTextContent('hará falta una sesión de monoes.me')
  })
})

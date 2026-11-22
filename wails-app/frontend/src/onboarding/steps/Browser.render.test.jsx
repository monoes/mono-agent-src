// @vitest-environment jsdom
// Step "Connect your browser": pick a Chromium browser, open the right
// browser profile, load the extension, pair it, then bind the attached
// browser to this MonoAgent profile. The bindings and the Wails event
// runtime are mocked; the tests assert what the person sees and which
// binding was called with what.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor, act } from '@testing-library/react'
import i18n from '../../i18n.js'

const listeners = {}
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn((name, cb) => { listeners[name] = cb; return () => { delete listeners[name] } }),
  ClipboardSetText: vi.fn(),
}))
vi.mock('../../wailsjs/go/main/App', () => ({
  DetectChromiumBrowsers: vi.fn(),
  OpenChromiumBrowser: vi.fn(),
  GetExtensionInfo: vi.fn(),
  InstallExtension: vi.fn(),
  PairExtension: vi.fn(),
  GetExtensionStatus: vi.fn(),
  GetBrowsers: vi.fn(),
  AssignBrowser: vi.fn(),
  GetActiveProfile: vi.fn(),
  RunHealthFix: vi.fn(),
  RunHealthCheck: vi.fn(),
  InstallAgentRuntime: vi.fn(),
  CancelHealthRun: vi.fn(),
}))

import * as Go from '../../wailsjs/go/main/App'
import * as Runtime from '../../wailsjs/runtime/runtime'
import Browser, { PairStep, PAIR_POLL_MS } from './Browser.jsx'

const B = (id, name, installed, url, side = 'right') => ({ id, name, installed, extensionsUrl: url, devModeSide: side })
const DETECTED = [
  B('chrome', 'Google Chrome', true, 'chrome://extensions'),
  B('edge', 'Microsoft Edge', true, 'edge://extensions', 'left'),
  B('brave', 'Brave', false, 'brave://extensions'),
  B('arc', 'Arc', false, 'arc://extensions'),
  B('vivaldi', 'Vivaldi', false, 'vivaldi://extensions'),
  B('opera', 'Opera', false, 'opera://extensions'),
  B('chromium', 'Chromium', false, 'chrome://extensions'),
]
const FOLDER = '/Users/someone/.monoagent/extension'
const status = (o) => JSON.stringify(o)
const CONNECTED = status({ running: true, bridge: { status: 'connected' }, clientPairing: 'ok' })
const row = (instance, label) => ({ instance, label, version: '1.4.0', legacy: false, profile_ids: [] })

let props
beforeEach(async () => {
  await i18n.changeLanguage('en')
  window.runtime = {}
  Go.DetectChromiumBrowsers.mockResolvedValue(DETECTED)
  Go.GetActiveProfile.mockResolvedValue({ id: 'p1', name: 'Work' })
  Go.OpenChromiumBrowser.mockResolvedValue(undefined)
  Go.GetExtensionInfo.mockResolvedValue({ path: FOLDER, installed: false, current: false, version: '' })
  Go.InstallExtension.mockResolvedValue({ path: FOLDER, installed: true, current: true, version: '1.4.0' })
  Go.GetExtensionStatus.mockResolvedValue(CONNECTED)
  Go.PairExtension.mockResolvedValue(JSON.stringify({ opened: true, url: 'http://127.0.0.1:9/pair' }))
  Go.GetBrowsers.mockResolvedValue({ running: true, browsers: [row('inst-aaaa1111', 'Work')] })
  Go.AssignBrowser.mockResolvedValue({ profile_id: 'p1', instance: 'inst-aaaa1111', changed: [] })
  Go.RunHealthFix.mockResolvedValue('{"ok":true}')
  props = {
    state: { completed: false, currentStep: 'browser', profileId: 'p1', steps: [{ id: 'browser', status: 'todo' }] },
    profileId: 'p1', mode: 'full',
    reload: vi.fn(() => Promise.resolve({ ok: true })),
    mark: vi.fn(), skip: vi.fn(), complete: vi.fn(),
    onDone: vi.fn(), onFinish: vi.fn(), goTo: vi.fn(),
    setCanContinue: vi.fn(), setOnContinue: vi.fn(), setContinueLabel: vi.fn(),
  }
})
afterEach(() => { cleanup(); vi.clearAllMocks(); delete window.runtime })

const show = () => render(<Browser {...props} />)
const click = (name) => fireEvent.click(screen.getByRole('button', { name }))
const radios = () => screen.getAllByRole('radio')

async function pick(name) {
  fireEvent.click(await screen.findByRole('radio', { name }))
}
async function toInstall(name) {
  await pick(name)
  click('Next')
  await screen.findByText('Click Load unpacked and choose this folder:')
}
async function toPair() {
  await toInstall(/Google Chrome/)
  await screen.findByText(FOLDER)
  click('I loaded the extension')
}
async function toConfirm() {
  await toPair()
  await screen.findByText('Connected.')
  click('Next')
}
const canContinueEver = () => props.setCanContinue.mock.calls.some(([v]) => v === true)

describe('Connect your browser', () => {
  it('lists installed browsers first and Other last', async () => {
    Go.DetectChromiumBrowsers.mockResolvedValue([DETECTED[2], DETECTED[0], DETECTED[1]])
    show()
    expect(screen.getByText('Works with any Chromium-based browser.')).toBeInTheDocument()
    await screen.findByRole('radio', { name: /Brave/ })
    const names = radios().map((r) => r.textContent)
    expect(names[0]).toMatch(/Google Chrome.*Installed/)
    expect(names[1]).toMatch(/Microsoft Edge.*Installed/)
    expect(names[2]).toMatch(/Brave.*Not found on this machine/)
    expect(names[names.length - 1]).toMatch(/Other Chromium browser/)
    expect(radios()).toHaveLength(4)
  })

  it('shows the chosen browser’s extensions address and Developer mode side', async () => {
    show()
    await pick(/Microsoft Edge/)
    expect(screen.getByText(/Open the Microsoft Edge profile whose logged-in accounts belong to Work/)).toBeInTheDocument()
    click('Open Microsoft Edge')
    await waitFor(() => expect(Go.OpenChromiumBrowser).toHaveBeenCalledWith('edge'))
    click('Next')
    // the address is shown to copy, and drawn in the illustration's address bar
    const addr = await screen.findAllByText('edge://extensions')
    expect(addr.map((el) => el.tagName.toLowerCase())).toEqual(['code', 'text'])
    expect(screen.getByRole('img', { name: /Developer mode switch in the left sidebar/ })).toBeInTheDocument()
    expect(screen.getByText('The switch is in the left sidebar.')).toBeInTheDocument()
    expect(screen.getAllByText(/Load unpacked/).length).toBeGreaterThan(0)
    expect(await screen.findByText(FOLDER)).toBeInTheDocument()
    expect(Go.InstallExtension).toHaveBeenCalledTimes(1)
  })

  it('Other shows the generic instruction and no Open button', async () => {
    show()
    await pick(/Other Chromium browser/)
    expect(screen.queryByRole('button', { name: /^Open / })).not.toBeInTheDocument()
    expect(screen.getByText(/Open the browser profile whose logged-in accounts belong to Work/)).toBeInTheDocument()
    click('Next')
    expect(await screen.findByText("Open your browser's extensions page (usually Menu, then Extensions).")).toBeInTheDocument()
    expect(screen.queryByText(/:\/\/extensions/)).not.toBeInTheDocument()
    expect(Go.OpenChromiumBrowser).not.toHaveBeenCalled()
  })

  it('service not running shows Start in place and pairing works after it starts', async () => {
    Go.GetExtensionStatus
      .mockResolvedValueOnce(status({ running: false, hint: 'No bridge is running.' }))
      .mockResolvedValue(status({ running: true, bridge: { status: 'waiting' }, clientPairing: 'ok' }))
    show()
    await toPair()
    expect(await screen.findByText('The background service is not running.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Pair extension' })).not.toBeInTheDocument()
    click('Start')
    await waitFor(() => expect(Go.RunHealthFix).toHaveBeenCalledWith('services.daemon.start'))
    await waitFor(() => expect(listeners['health:fixProgress']).toBeTypeOf('function'))
    act(() => { listeners['health:fixProgress']({ fix_id: 'services.daemon.start', kind: 'line', message: 'daemon started' }) })
    expect(await screen.findByText('daemon started')).toBeInTheDocument()
    act(() => { listeners['health:fixProgress']({ fix_id: 'services.daemon.start', kind: 'done' }) })
    expect(await screen.findByText(/Waiting for the extension to connect/)).toBeInTheDocument()
    expect(screen.queryByText('The background service is not running.')).not.toBeInTheDocument()
    click('Pair extension')
    await waitFor(() => expect(Go.PairExtension).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('The pairing page opened in your browser. Approve it there.')).toBeInTheDocument()
  })

  it('one attached browser is confirmed with AssignBrowser(profileId, instance)', async () => {
    show()
    await toConfirm()
    expect(await screen.findByText(/Work · inst-aaa/)).toBeInTheDocument()
    expect(screen.getByText('Agents working in Work will only use this browser profile.')).toBeInTheDocument()
    expect(screen.queryAllByRole('radio')).toHaveLength(0)
    click('Use this browser')
    await waitFor(() => expect(Go.AssignBrowser).toHaveBeenCalledWith('p1', 'inst-aaaa1111'))
    await waitFor(() => expect(props.reload).toHaveBeenCalled())
    await waitFor(() => expect(props.setCanContinue).toHaveBeenLastCalledWith(true))
  })

  it('several attached browsers are offered as a list', async () => {
    Go.GetBrowsers.mockResolvedValue({ running: true, browsers: [row('inst-aaaa1111', 'Work'), row('inst-bbbb2222', 'Personal')] })
    show()
    await toConfirm()
    await screen.findByRole('radio', { name: /Personal/ })
    expect(radios()).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Use this browser' })).toBeDisabled()
    fireEvent.click(screen.getByRole('radio', { name: /Personal/ }))
    click('Use this browser')
    await waitFor(() => expect(Go.AssignBrowser).toHaveBeenCalledWith('p1', 'inst-bbbb2222'))
  })

  it('Continue stays disabled until a browser is assigned', async () => {
    Go.AssignBrowser.mockRejectedValueOnce('extension assign: the browser went away')
    show()
    await toConfirm()
    await screen.findByText(/Work · inst-aaa/)
    expect(canContinueEver()).toBe(false)
    click('Use this browser')
    expect(await screen.findByText('extension assign: the browser went away')).toBeInTheDocument()
    expect(canContinueEver()).toBe(false)
    expect(props.reload).not.toHaveBeenCalled()
    click('Use this browser')
    await waitFor(() => expect(props.setCanContinue).toHaveBeenLastCalledWith(true))
  })

  it('Copy buttons write the address and the folder to the clipboard', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    show()
    await toInstall(/Google Chrome/)
    await screen.findByText(FOLDER)
    click('Copy the address')
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('chrome://extensions'))
    click('Copy the folder')
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(FOLDER))
    expect((await screen.findAllByText('Copied')).length).toBeGreaterThan(0)
  })

  it('an extension already unpacked is not unpacked again; Reinstall does it on request', async () => {
    Go.GetExtensionInfo.mockResolvedValue({ path: FOLDER, installed: true, current: true, version: '1.4.0' })
    show()
    await toInstall(/Google Chrome/)
    expect(await screen.findByText(FOLDER)).toBeInTheDocument()
    expect(Go.GetExtensionInfo).toHaveBeenCalledTimes(1)
    expect(Go.InstallExtension).not.toHaveBeenCalled()
    click('Reinstall')
    await waitFor(() => expect(Go.InstallExtension).toHaveBeenCalledTimes(1))
  })

  it('a copy other than the shipped one (for example the person’s own zip) is kept until Reinstall', async () => {
    Go.GetExtensionInfo.mockResolvedValue({ path: FOLDER, installed: true, current: false, version: '1.0.0' })
    show()
    await toInstall(/Google Chrome/)
    expect(await screen.findByText(/copy of the extension other than the one this app ships/)).toBeInTheDocument()
    expect(screen.getByText(FOLDER)).toBeInTheDocument()
    expect(Go.InstallExtension).not.toHaveBeenCalled()
    click('Reinstall')
    await waitFor(() => expect(Go.InstallExtension).toHaveBeenCalledTimes(1))
  })

  it('a service that starts but leaves the bridge off shows the hint and Restart, not Start again', async () => {
    const hint = 'No bridge is running. Start one with `monoagentcli extension serve`.'
    Go.GetExtensionStatus.mockResolvedValue(status({ running: false, hint }))
    show()
    await toPair()
    await screen.findByRole('button', { name: 'Start' })
    click('Start')
    await waitFor(() => expect(listeners['health:fixProgress']).toBeTypeOf('function'))
    act(() => { listeners['health:fixProgress']({ fix_id: 'services.daemon.start', kind: 'line', message: 'the daemon is already running' }) })
    act(() => { listeners['health:fixProgress']({ fix_id: 'services.daemon.start', kind: 'done' }) })
    // the first off read after a start is a grace period: the bridge may only be slow
    expect(await screen.findByText("The service started. Waiting for the extension's bridge to come up…")).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Restart the service' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Start' })).not.toBeInTheDocument()
    // the next poll still reads off: now it is stuck
    expect(await screen.findByText("The background service is running, but the extension's bridge is not answering.")).toBeInTheDocument()
    expect(screen.getByText(hint)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Start' })).not.toBeInTheDocument()
    // the first click only asks, naming what a restart interrupts
    click('Restart the service')
    expect(screen.getByText('Restarting stops the background service and starts it again. It interrupts whatever it is running: workflows and org runs.')).toBeInTheDocument()
    expect(Go.RunHealthFix).not.toHaveBeenCalledWith('services.daemon.restart')
    click('Cancel')
    expect(screen.queryByText(/It interrupts whatever it is running/)).not.toBeInTheDocument()
    expect(Go.RunHealthFix).not.toHaveBeenCalledWith('services.daemon.restart')
    click('Restart the service')
    click('Restart anyway')
    await waitFor(() => expect(Go.RunHealthFix).toHaveBeenLastCalledWith('services.daemon.restart'))
  })

  it('a bridge that comes up during the grace period never shows the stuck state', async () => {
    Go.GetExtensionStatus
      .mockResolvedValueOnce(status({ running: false }))
      .mockResolvedValueOnce(status({ running: false }))
      .mockResolvedValue(status({ running: true, bridge: { status: 'waiting' }, clientPairing: 'ok' }))
    show()
    await toPair()
    await screen.findByRole('button', { name: 'Start' })
    click('Start')
    await waitFor(() => expect(listeners['health:fixProgress']).toBeTypeOf('function'))
    act(() => { listeners['health:fixProgress']({ fix_id: 'services.daemon.start', kind: 'done' }) })
    expect(await screen.findByText("The service started. Waiting for the extension's bridge to come up…")).toBeInTheDocument()
    expect(await screen.findByText(/Waiting for the extension to connect/)).toBeInTheDocument()
    expect(screen.queryByText(/bridge is not answering/)).not.toBeInTheDocument()
  })

  it('a clipboard that refuses says to copy by hand', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn(() => Promise.reject(new Error('denied'))) }, configurable: true })
    Runtime.ClipboardSetText.mockRejectedValue(new Error('no runtime'))
    show()
    await toInstall(/Google Chrome/)
    click('Copy the address')
    expect(await screen.findByText('Could not copy. Select the text and copy it yourself.')).toBeInTheDocument()
    expect(Runtime.ClipboardSetText).toHaveBeenCalledWith('chrome://extensions')
    expect(screen.queryByText('Copied')).not.toBeInTheDocument()
  })

  it('the Wails clipboard is used when the web one is missing', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    Runtime.ClipboardSetText.mockResolvedValue(true)
    show()
    await toInstall(/Google Chrome/)
    click('Copy the address')
    expect(await screen.findByText('Copied')).toBeInTheDocument()
  })

  it('lists which profiles already use each attached browser', async () => {
    Go.GetBrowsers.mockResolvedValue({ running: true, browsers: [{ ...row('inst-aaaa1111', 'Work'), profile_names: ['Client A', 'Client B'] }, row('inst-bbbb2222', 'Personal')] })
    show()
    await toConfirm()
    expect(await screen.findByText('Already used by: Client A, Client B')).toBeInTheDocument()
  })

  it('a sign-in refusal shows the sign-in message and the button works again', async () => {
    Go.OpenChromiumBrowser.mockRejectedValueOnce('Log in to monoes.me first: run `monoagentcli account login`.')
    show()
    await pick(/Google Chrome/)
    click('Open Google Chrome')
    expect(await screen.findByText(i18n.t('onboarding.shell.signIn'))).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Open Google Chrome' })).toBeEnabled()
  })
})

describe('the pair sub-step', () => {
  const WAITING = status({ running: true, bridge: { status: 'waiting' }, clientPairing: 'ok' })

  it('waits for each status answer before the next read, and stops on unmount', async () => {
    vi.useFakeTimers()
    try {
      const answers = []
      Go.GetExtensionStatus.mockImplementation(() => new Promise((r) => answers.push(r)))
      const { unmount } = render(<PairStep onNext={() => {}} />)
      expect(Go.GetExtensionStatus).toHaveBeenCalledTimes(1)
      // a slow CLI: no second read while the first is unanswered
      await act(async () => { vi.advanceTimersByTime(PAIR_POLL_MS * 5) })
      expect(Go.GetExtensionStatus).toHaveBeenCalledTimes(1)
      await act(async () => { answers[0](WAITING) })
      await act(async () => { vi.advanceTimersByTime(PAIR_POLL_MS - 1) })
      expect(Go.GetExtensionStatus).toHaveBeenCalledTimes(1)
      await act(async () => { vi.advanceTimersByTime(1) })
      expect(Go.GetExtensionStatus).toHaveBeenCalledTimes(2)
      await act(async () => { answers[1](WAITING) })
      unmount()
      await act(async () => { vi.advanceTimersByTime(PAIR_POLL_MS * 5) })
      expect(Go.GetExtensionStatus).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('an answer older than the newest read is dropped', async () => {
    const answers = []
    Go.GetExtensionStatus.mockImplementation(() => new Promise((r) => answers.push(r)))
    Go.PairExtension.mockResolvedValue(JSON.stringify({ opened: true }))
    render(<PairStep onNext={() => {}} />)
    await act(async () => { answers[0](WAITING) })
    click('Pair extension')
    await waitFor(() => expect(answers).toHaveLength(2))
    await screen.findByText('The pairing page opened in your browser. Approve it there.')
    click('Pair extension')
    await waitFor(() => expect(answers).toHaveLength(3))
    // the newest read answers first; the older one, arriving late, is dropped
    await act(async () => { answers[2](WAITING) })
    await act(async () => { answers[1](CONNECTED) })
    expect(screen.queryByText('Connected.')).not.toBeInTheDocument()
    expect(screen.getByText(/Waiting for the extension to connect/)).toBeInTheDocument()
  })

  it.each([
    ['mismatch', { running: true, bridge: { status: 'connected' }, clientPairing: 'mismatch' }],
    ['unpaired', { running: true, bridge: { status: 'unpaired' }, clientPairing: 'ok' }],
    ['unknown (a bridge too old to tell)', { running: true, bridge: { status: 'connected' }, clientPairing: 'unknown' }],
  ])('pairing %s reads as not paired', async (_, st) => {
    Go.GetExtensionStatus.mockResolvedValue(status(st))
    render(<PairStep onNext={() => {}} />)
    expect(await screen.findByText('The extension is not paired with MonoAgent yet.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pair extension' })).toBeInTheDocument()
    expect(screen.queryByText('Connected.')).not.toBeInTheDocument()
  })

  it('a sign-in refusal of the status still offers Check again', async () => {
    Go.GetExtensionStatus
      .mockResolvedValueOnce(JSON.stringify({ error: 'Log in to monoes.me first', login_required: true }))
      .mockResolvedValue(CONNECTED)
    render(<PairStep onNext={() => {}} />)
    expect(await screen.findByText(i18n.t('onboarding.shell.signIn'))).toBeInTheDocument()
    click('Check again')
    expect(await screen.findByText('Connected.')).toBeInTheDocument()
  })
})

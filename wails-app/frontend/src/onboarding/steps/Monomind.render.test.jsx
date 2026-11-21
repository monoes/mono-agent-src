// @vitest-environment jsdom
// Turn on monomind: the success state when the profile is already set up, the
// streamed progress lines, the sign-in and failure answers, and a result that
// lands after the person moved on to another step.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, act } from '@testing-library/react'
import i18n from '../../i18n.js'

const { go, events } = vi.hoisted(() => ({
  go: {
    IsMonomindInitialized: vi.fn(),
    InitializeMonomindProfile: vi.fn(),
    GetActiveProfile: vi.fn(),
  },
  events: {},
}))
vi.mock('../../wailsjs/go/main/App', () => go)
vi.mock('../../services/api.js', () => ({
  api: {},
  subscribeEvent: (name, cb) => {
    events[name] = cb
    return () => { if (events[name] === cb) delete events[name] }
  },
}))

import Monomind from './Monomind.jsx'
import { resetMonomindInit } from '../monomindInit.js'

const emit = (payload) => act(() => { events['monomind:initProgress']?.(payload) })

let props
beforeEach(async () => {
  await i18n.changeLanguage('en')
  vi.clearAllMocks()
  resetMonomindInit()
  go.IsMonomindInitialized.mockResolvedValue(false)
  go.InitializeMonomindProfile.mockResolvedValue('{"ok":true}')
  go.GetActiveProfile.mockResolvedValue({ id: 'work', name: 'Work' })
  props = {
    state: { profileId: 'work', steps: [] },
    profileId: 'work',
    mode: 'full',
    reload: vi.fn(() => Promise.resolve({ ok: true })),
    setCanContinue: vi.fn(),
    setOnContinue: vi.fn(),
    setContinueLabel: vi.fn(),
    goTo: vi.fn(),
  }
})
afterEach(cleanup)

const lastCanContinue = () => props.setCanContinue.mock.calls.at(-1)?.[0]

describe('Monomind step', () => {
  it('explains documents and orgs and names the profile on the button', async () => {
    render(<Monomind {...props} />)
    expect(screen.getByText('monomind is the engine behind every AI step in MonoAgent.')).toBeInTheDocument()
    expect(screen.getByText('For documents')).toBeInTheDocument()
    expect(screen.getByText('For orgs')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Turn on for Work' })).toBeEnabled()
    expect(screen.getByText("You'll find this later in the Documents tab.")).toBeInTheDocument()
    expect(lastCanContinue()).toBe(false)
  })

  it('shows the success state at once when already initialized', async () => {
    go.IsMonomindInitialized.mockResolvedValue(true)
    render(<Monomind {...props} />)
    expect(await screen.findByText('monomind is on for Work.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Turn on for/ })).not.toBeInTheDocument()
    expect(lastCanContinue()).toBe(true)
    expect(go.InitializeMonomindProfile).not.toHaveBeenCalled()
  })

  it('shows each progress event as a line while running', async () => {
    render(<Monomind {...props} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Turn on for Work' }))
    expect(await screen.findByText('Turning on monomind for Work')).toBeInTheDocument()
    expect(go.InitializeMonomindProfile).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument()

    emit({ kind: 'line', message: 'monomind init: writing .monomind/config.yaml' })
    emit({ kind: 'line', message: 'registering with Claude Code' })
    const items = screen.getAllByRole('listitem').map(li => li.textContent)
    expect(items).toEqual(['monomind init: writing .monomind/config.yaml', 'registering with Claude Code'])
    expect(lastCanContinue()).toBe(false)

    emit({ kind: 'done', message: '' })
    expect(await screen.findByText('monomind is on for Work.')).toBeInTheDocument()
    expect(lastCanContinue()).toBe(true)
    expect(props.reload).toHaveBeenCalledTimes(1)
  })

  it('a login-required return value shows the sign-in message and re-enables the button', async () => {
    go.InitializeMonomindProfile.mockResolvedValue(JSON.stringify({ error: 'Log in to monoes.me first: monoagentcli account login', login_required: true }))
    render(<Monomind {...props} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Turn on for Work' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Sign in to monoes.me first, then try again.')
    expect(screen.getByRole('button', { name: 'Turn on for Work' })).toBeEnabled()
    expect(screen.queryByText('Turning on monomind for Work')).not.toBeInTheDocument()
    expect(lastCanContinue()).toBe(false)
  })

  it('a failed init shows the error and re-enables the button', async () => {
    render(<Monomind {...props} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Turn on for Work' }))
    await screen.findByText('Turning on monomind for Work')
    emit({ kind: 'line', message: 'starting' })
    emit({ kind: 'error', message: 'monomind init exited with status 1' })
    expect(await screen.findByRole('alert')).toHaveTextContent('monomind could not be turned on: monomind init exited with status 1')
    expect(screen.getByRole('button', { name: 'Turn on for Work' })).toBeEnabled()
    expect(props.reload).not.toHaveBeenCalled()
    expect(lastCanContinue()).toBe(false)
  })

  it('ignores progress events from a run it did not start', async () => {
    render(<Monomind {...props} />)
    await screen.findByRole('button', { name: 'Turn on for Work' })
    emit({ kind: 'line', message: 'from the Documents page' })
    emit({ kind: 'done', message: '' })
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Turn on for Work' })).toBeInTheDocument()
  })

  it('remounting while nothing is running shows the button, not a progress view', async () => {
    const first = render(<Monomind {...props} />)
    await screen.findByRole('button', { name: 'Turn on for Work' })
    first.unmount()
    resetMonomindInit() // the app was closed mid-init: nothing in this window is running
    render(<Monomind {...props} />)
    expect(await screen.findByRole('button', { name: 'Turn on for Work' })).toBeEnabled()
    expect(screen.queryByText('Turning on monomind for Work')).not.toBeInTheDocument()
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument()
  })

  it('remounting while a run is going shows its progress, not a second button', async () => {
    const first = render(<Monomind {...props} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Turn on for Work' }))
    await screen.findByText('Turning on monomind for Work')
    emit({ kind: 'line', message: 'step one' })
    first.unmount()
    render(<Monomind {...props} />)
    expect(await screen.findByText('Turning on monomind for Work')).toBeInTheDocument()
    expect(screen.getByRole('listitem')).toHaveTextContent('step one')
    expect(screen.queryByRole('button', { name: /Turn on for/ })).not.toBeInTheDocument()
  })

  it('success after the user left the step still reloads state', async () => {
    const view = render(<Monomind {...props} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Turn on for Work' }))
    await screen.findByText('Turning on monomind for Work')
    view.rerender(<div>another step</div>)
    expect(props.reload).not.toHaveBeenCalled()
    emit({ kind: 'done', message: '' })
    expect(props.reload).toHaveBeenCalledTimes(1)
  })

  it('falls back to the profile id when the profile cannot be read', async () => {
    go.GetActiveProfile.mockRejectedValue('Log in to monoes.me first: monoagentcli account login')
    render(<Monomind {...props} />)
    expect(await screen.findByRole('button', { name: 'Turn on for work' })).toBeInTheDocument()
  })
})

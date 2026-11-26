// @vitest-environment jsdom
// The two chat warnings: a new chat about to run at full access, and a turn
// that runs without a sandbox (its agent.sandbox notice is a warning).
import { render, screen, cleanup, act } from '@testing-library/react'
import { describe, it, expect, afterEach, vi } from 'vitest'
import i18n from '../../i18n.js'
import { chatReducer, initialChatState } from './chatReducer.js'
import { reduceTurnEvents } from './useChatStream.js'
import { ChatTimeline } from './ChatTimeline.jsx'
import { FetchedAccessWarning } from './ChatWarnings.jsx'
import { LiteTurn } from '../bubbles/LiteTurn.jsx'
import { ChatSetup } from '../bubbles/ChatSetup.jsx'
import { QueuedCoderBubble } from '../bubbles/QueuedCoderBubble.jsx'

vi.mock('../../services/api.js', () => ({
  api: {
    coderQueueImage: () => Promise.resolve(null),
    coderStatus: () => Promise.resolve({ enabled: true, box: 'off' }),
    coderRecentWorkspaces: () => Promise.resolve([]),
    pickCoderFolder: () => Promise.resolve(''),
  },
  notify: () => {},
}))

const FULL = /Full access: this chat can run any command and change any file on this computer as you\./
const NO_SANDBOX = /No sandbox for this turn: the agent is not confined to a working folder\./

afterEach(async () => {
  cleanup()
  await act(() => i18n.changeLanguage('en'))
})

const setupState = access => ({
  settled: true, status: { enabled: true }, coderOk: true, access, coder: false,
  options: [], runtime: '', models: [], efforts: [], state: 'ready',
})

describe('full access warning', () => {
  const draw = access => <ChatSetup s={setupState(access)} setup={{ access }} onChange={() => {}} />

  it('shows on a draft at full access and goes when the level is lowered', () => {
    const { rerender } = render(draw('full'))
    expect(screen.getByRole('alert').textContent).toMatch(FULL)
    for (const lower of ['workspace', 'coding', 'scoped']) {
      rerender(draw(lower))
      expect(screen.queryByText(FULL)).toBeNull()
    }
    rerender(draw('full'))
    expect(screen.getByRole('alert').textContent).toMatch(FULL)
  })

  it('is said in Spanish', async () => {
    await act(() => i18n.changeLanguage('es'))
    render(draw('full'))
    expect(screen.getByRole('alert').textContent).toMatch(/Acceso completo/)
  })

  it('shows on a queued coder chat, which always starts at full access', () => {
    render(<QueuedCoderBubble item={{ id: 'q1', kind: 'ask', origin: 'https://example.com', path: '/' }} onStart={() => {}} onDismiss={() => {}} />)
    expect(screen.getByText(FULL).closest('[role="status"]')).toBeTruthy()
  })
})

const BOX_OFF = level => new RegExp(`${level} access is not enforced because the coder box is off: this chat can run any command and change any file on this computer as you\\.`)

describe('coding and workspace while the coder box is off', () => {
  const draw = (access, status) => <ChatSetup s={{ ...setupState(access), status }} setup={{ access }} onChange={() => {}} />

  for (const [access, level] of [['coding', 'Coding'], ['workspace', 'Workspace']]) {
    it(`warns on a ${access} draft, naming the level`, () => {
      render(draw(access, { enabled: true, box: 'off' }))
      expect(screen.getByRole('alert').textContent).toMatch(BOX_OFF(level))
    })
  }

  it('warns when an older CLI reports no box at all', () => {
    render(draw('coding', { enabled: true }))
    expect(screen.getByRole('alert').textContent).toMatch(BOX_OFF('Coding'))
  })

  it('says nothing when the box is on, at scoped, or when the status could not be read', () => {
    for (const [access, status] of [
      ['coding', { enabled: true, box: 'compat' }], ['coding', { enabled: true, box: 'strict' }],
      ['workspace', { enabled: true, box: 'strict' }], ['scoped', { enabled: true, box: 'off' }], ['coding', null],
    ]) {
      render(draw(access, status))
      expect(screen.queryByRole('alert')).toBeNull()
      cleanup()
    }
  })

  it('reads coder status itself where the view has none (the open-ended start screen)', async () => {
    render(<FetchedAccessWarning access="workspace" />)
    expect((await screen.findByRole('alert')).textContent).toMatch(BOX_OFF('Workspace'))
  })

  it('is said in Spanish', async () => {
    await act(() => i18n.changeLanguage('es'))
    render(draw('coding', { enabled: true, box: 'off' }))
    expect(screen.getByRole('alert').textContent).toMatch(/no se aplica/)
  })
})

const NO_SANDBOX_WORKER = /No sandbox for this agent: its runtime cannot limit it to reading, so it has full access and is only told not to change files\./

describe('unconfined research worker warning', () => {
  const events = confinement => [
    { type: 'agent.spawned', seq: 1, payload: { agentId: 'w1', role: 'scout', agentType: 'worker', runtime: 'copilot' } },
    { type: 'agent.status', seq: 2, payload: { agentId: 'w1', from: 'starting', to: 'working', confinement } },
    { type: 'agent.status', seq: 3, payload: { agentId: 'w1', from: 'working', to: 'done' } },
  ]

  it('shows on the worker row of a turn, and stays once it is done', () => {
    render(<ChatTimeline state={reduceTurnEvents(events('write-lease'))} turnId="t1" />)
    expect(screen.getByText(NO_SANDBOX_WORKER).closest('[role="status"]')).toBeTruthy()
  })

  it('is absent for a confined worker', () => {
    for (const confinement of ['access-read', 'sandbox-read-only', undefined]) {
      render(<ChatTimeline state={reduceTurnEvents(events(confinement))} turnId="t1" />)
      expect(screen.getByTestId('agent-row')).toBeTruthy()
      expect(screen.queryByText(NO_SANDBOX_WORKER)).toBeNull()
      cleanup()
    }
  })
})

const notice = (severity, message = 'unsupported') => ({
  type: 'notice', seq: 1, at: '2026-11-26T10:00:00Z', payload: { code: 'agent.sandbox', message, severity },
})

describe('unsandboxed turn warning', () => {
  for (const [name, draw] of [
    ['ChatTimeline', state => <ChatTimeline state={state} turnId="t1" />],
    ['LiteTurn', state => <LiteTurn state={state} turnId="t1" />],
  ]) {
    it(`shows on a turn in ${name} when the CLI marks its sandbox notice a warning`, () => {
      render(draw(reduceTurnEvents([notice('warning')])))
      expect(screen.getByText(NO_SANDBOX).closest('[role="status"]')).toBeTruthy()
    })

    it(`is absent in ${name} for a sandboxed turn`, () => {
      render(draw(reduceTurnEvents([notice('info', 'sandboxed')])))
      expect(screen.queryByText(NO_SANDBOX)).toBeNull()
    })
  }

  it('stays after the turn finishes and starts off', () => {
    expect(initialChatState().sandboxMissing).toBeFalsy()
    const state = reduceTurnEvents([notice('warning'), { type: 'turn.finished', seq: 2, payload: { status: 'completed', sandbox: 'unsupported' } }])
    expect(state.sandboxMissing).toBe(true)
    expect(chatReducer(state, { type: 'reset' }).sandboxMissing).toBeFalsy()
  })
})

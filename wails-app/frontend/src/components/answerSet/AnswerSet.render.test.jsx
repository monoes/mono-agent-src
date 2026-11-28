// @vitest-environment jsdom
// Several questions waiting at once are answered together (#1803): worker
// question cards, agent cards and chat blocks send nothing on their own, and
// what was typed survives new questions, timeouts and a redraw.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, configure, within } from '@testing-library/react'
import '../../i18n.js'

configure({ asyncUtilTimeout: 8000 })
vi.setConfig({ testTimeout: 20000 })

const { api, notify } = vi.hoisted(() => ({
  api: { chatUIAction: vi.fn(), answerAgentQuestion: vi.fn(), chatUISyncHIL: vi.fn(() => Promise.resolve({ surfaced: [], resolved: [] })) },
  notify: vi.fn(),
}))
vi.mock('../../services/api.js', () => ({
  api: new Proxy(api, { get: (t, k) => (k in t ? t[k] : () => Promise.resolve([])) }),
  notify,
  onChatEvent: () => () => {},
}))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: { list: () => Promise.resolve({ sessions: [] }) } }))
vi.mock('../bubbles/useHilAsks.js', () => ({ useHilAsks: () => ({ asks: [], busy: {}, outcome: {}, resolve: vi.fn() }) }))
vi.mock('../bubbles/PersonItemViews.jsx', () => ({ PersonItemsRegion: () => null }))
vi.mock('../bubbles/mission/MissionStrip.jsx', () => ({ default: () => null }))

import { CoderChatView } from '../bubbles/CoderChatView.jsx'
import { chatReducer, initialChatState } from '../chat/chatReducer.js'
import { clearDrafts } from '../../lib/answerDrafts.js'

const v = m => ({ version: '1.0', ...m })
const text = (id, t) => ({ id, component: 'Text', text: t })
const button = (id, label, name, context = {}) => [text(`${id}-l`, label), { id, component: 'Button', child: `${id}-l`, action: { event: { name, context } } }]

// The host's card of a worker's question, and the event that opened it.
const ask = (agent, question) => {
  const id = `${agent}.q1`
  return [
    ['agent.spawned', { agentId: agent, role: agent }],
    ['agent.status', { agentId: agent, status: 'waiting_user' }],
    ['agent.message', { agentId: agent, direction: 'question', questionId: 'q1', from: agent, to: 'user', text: question }],
    ['ui.surface', { surfaceId: `ask-${id}`, provenance: 'system', ref: { kind: 'ask', id }, message: v({ createSurface: {
      surfaceId: `ask-${id}`, dataModel: { answers: { q0: '' } },
      components: [{ id: 'root', component: 'Column', children: ['q-q0', 'in-q0', 'go'] }, text('q-q0', question),
        { id: 'in-q0', component: 'TextField', label: `Answer for ${agent}`, value: { '@path': '/answers/q0' } },
        ...button('go', 'Continue', 'system.answer', { questionId: id, q0: { '@path': '/answers/q0' } })] } }) }],
  ]
}
const closed = agent => [
  ['ui.surface', { surfaceId: `ask-${agent}.q1`, provenance: 'system', ref: { kind: 'ask', id: `${agent}.q1` }, message: v({ deleteSurface: { surfaceId: `ask-${agent}.q1` } }) }],
  ['ui.surface', { surfaceId: `ask-${agent}.q1`, provenance: 'system', ref: { kind: 'ask', id: `${agent}.q1` }, message: v({ createSurface: {
    surfaceId: `ask-${agent}.q1`, components: [text('root', 'Closed without an answer')] } }) }],
]
const card = id => ['ui.surface', { surfaceId: id, provenance: 'agent', message: v({ createSurface: {
  surfaceId: id, components: [{ id: 'root', component: 'Column', children: ['t', 'yes', 'no'] }, text('t', `Question ${id}`),
    ...button('yes', `Yes ${id}`, `yes_${id}`), ...button('no', `No ${id}`, `no_${id}`)] } }) }]

const turnState = (turnId, evs) => evs.reduce((s, [type, payload], i) => chatReducer(s, { type: 'event', event: { conversationId: 'chat-1', turnId, seq: i + 1, at: '2026-11-28T10:00:00Z', type, payload } }),
  chatReducer(initialChatState(), { type: 'scope', scope: { conversationId: 'chat-1', turnId } }))
const done = ['turn.finished', { status: 'completed' }]
const base = { conversationId: 'chat-1', messages: [], liveTurn: { calls: {}, lastSeq: 0 }, activeTurnId: '', streaming: false, loading: false, loadError: '', announcement: '', stop: () => {}, model: '' }
const mk = over => ({ ...base, send: vi.fn(() => Promise.resolve(true)), refreshUI: vi.fn(), ...over })
const live = evs => mk({ streaming: true, activeTurnId: 't1', liveTurn: turnState('t1', evs) })
const finished = evs => mk({ messages: [{ role: 'turn', turnId: 't1', state: turnState('t1', [...evs, done]) }] })
const view = c => <CoderChatView conv={c} isDraft={false} access="full" setup={{ workspace: { kind: 'root' }, runtime: '', model: '', effort: '' }} onSetupChange={() => {}} draft="" onDraftChange={() => {}} />

beforeEach(() => { Element.prototype.scrollTo = function scrollTo() {}; localStorage.clear(); clearDrafts() })
afterEach(() => { cleanup(); vi.clearAllMocks() })

const field = agent => screen.getByRole('textbox', { name: `Answer for ${agent}` })
const type = (agent, value) => fireEvent.change(field(agent), { target: { value } })
const bar = () => screen.getByTestId('answer-set-bar')
const sendAll = () => within(bar()).getByRole('button', { name: 'Send all' })

describe('several worker questions', () => {
  const two = [...ask('w1', 'Which region?'), ...ask('w2', 'Which database?')]

  it('sends nothing until every one is answered, then all in one press', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'answer' })
    const c = live(two)
    render(view(c))
    expect(screen.queryByRole('button', { name: 'Continue' })).toBeNull()
    // The rows' own Answer boxes are the cards' now.
    expect(screen.queryByRole('button', { name: 'Answer' })).toBeNull()
    expect(sendAll()).toBeDisabled()
    type('w1', 'eu')
    fireEvent.keyDown(field('w1'), { key: 'Enter' })
    fireEvent.click(sendAll())
    expect(api.chatUIAction).not.toHaveBeenCalled()
    expect(bar()).toHaveTextContent('1 of 2 ready')
    type('w2', 'postgres')
    expect(api.chatUIAction).not.toHaveBeenCalled()
    fireEvent.click(sendAll())
    await waitFor(() => expect(api.chatUIAction).toHaveBeenCalledTimes(1))
    const env = api.chatUIAction.mock.calls[0][1]
    expect(env.actions.map(a => [a.surfaceId, a.context.q0])).toEqual([['ask-w1.q1', 'eu'], ['ask-w2.q1', 'postgres']])
    expect(api.answerAgentQuestion).not.toHaveBeenCalled()
    expect(c.send).not.toHaveBeenCalled()
  })

  it('keeps what was typed when a question arrives, and sends nothing', () => {
    const one = ask('w1', 'Which region?')
    const { rerender } = render(view(live(one)))
    expect(screen.getByRole('button', { name: 'Continue' })).toHaveAttribute('aria-disabled', 'true')
    type('w1', 'eu')
    expect(screen.getByRole('button', { name: 'Continue' })).not.toHaveAttribute('aria-disabled')
    const input = field('w1')
    // The second worker's row lands after the first card in the turn.
    rerender(view(live(two)))
    expect(field('w1')).toBe(input)
    expect(input).toHaveValue('eu')
    expect(screen.queryByRole('button', { name: 'Continue' })).toBeNull()
    expect(sendAll()).toBeDisabled()
    expect(api.chatUIAction).not.toHaveBeenCalled()
  })

  it('goes back to one card when the other times out, without sending it', () => {
    const { rerender } = render(view(live(two)))
    type('w1', 'eu')
    type('w2', 'pg')
    rerender(view(live([...two, ...closed('w2')])))
    expect(screen.queryByTestId('answer-set-bar')).toBeNull()
    expect(field('w1')).toHaveValue('eu')
    expect(screen.getByRole('button', { name: 'Continue' })).toBeInTheDocument()
    expect(api.chatUIAction).not.toHaveBeenCalled()
  })

  it('shows what was typed again after the chat is drawn anew', () => {
    const first = render(view(live(two)))
    type('w1', 'eu')
    first.unmount()
    render(view(live(two)))
    expect(field('w1')).toHaveValue('eu')
    expect(api.chatUIAction).not.toHaveBeenCalled()
  })

  it('keeps every answer when the set is refused', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'rejected', reason: 'nothing was sent: another card is waiting too' })
    render(view(live(two)))
    type('w1', 'eu')
    type('w2', 'pg')
    fireEvent.click(sendAll())
    await waitFor(() => expect(notify).toHaveBeenCalledWith('a2ui', expect.stringContaining('nothing was sent'), '', expect.any(String)))
    await waitFor(() => expect(sendAll()).toBeEnabled())
    expect(field('w1')).toHaveValue('eu')
    expect(field('w2')).toHaveValue('pg')
  })

  it('keeps the questions reachable when the transcript is filtered to one agent', () => {
    render(<CoderChatView conv={live(two)} agentFilter="w1" agentFilterTurnId="t1" isDraft={false} access="full" setup={{ workspace: { kind: 'root' } }} onSetupChange={() => {}} draft="" onDraftChange={() => {}} />)
    expect(field('w2')).toBeInTheDocument()
    expect(sendAll()).toBeDisabled()
  })
})

describe('several agent cards', () => {
  const two = [card('region'), card('db')]

  it('picks without sending, then sends one message for the set', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'next_turn', message: 'both' })
    const c = finished(two)
    render(view(c))
    fireEvent.click(screen.getByRole('button', { name: 'Yes region' }))
    expect(screen.getByRole('button', { name: 'Yes region' })).toHaveAttribute('aria-pressed', 'true')
    expect(api.chatUIAction).not.toHaveBeenCalled()
    expect(sendAll()).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'No db' }))
    fireEvent.click(sendAll())
    await waitFor(() => expect(c.send).toHaveBeenCalledWith('both', []))
    expect(api.chatUIAction).toHaveBeenCalledTimes(1)
    expect(api.chatUIAction.mock.calls[0][1].actions.map(a => a.name)).toEqual(['yes_region', 'no_db'])
  })

  it('leaves a card out only when the person skips it', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'next_turn', message: 'one' })
    render(view(finished(two)))
    fireEvent.click(screen.getByRole('button', { name: 'Yes region' }))
    fireEvent.click(screen.getAllByRole('button', { name: 'Skip this one' })[1])
    fireEvent.click(sendAll())
    await waitFor(() => expect(api.chatUIAction).toHaveBeenCalledTimes(1))
    expect(api.chatUIAction.mock.calls[0][1]).toMatchObject({ actions: [{ name: 'yes_region' }], skipped: ['db'] })
  })

  it('sends a single card at once, as before', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'answer' })
    render(view(finished([card('region')])))
    fireEvent.click(screen.getByRole('button', { name: 'Yes region' }))
    await waitFor(() => expect(api.chatUIAction.mock.calls[0][1].action.name).toBe('yes_region'))
  })

  it('sends every message pressed while a turn runs, in order', async () => {
    api.chatUIAction.mockImplementation((_, env) => Promise.resolve({ outcome: 'accepted', delivered: 'next_turn', message: env.action.name }))
    const turn = (id, c) => ({ role: 'turn', turnId: id, state: turnState(id, [card(c), done]) })
    const c = mk({ messages: [turn('t1', 'region'), turn('t2', 'db')], streaming: true, activeTurnId: 't3', liveTurn: turnState('t3', []) })
    const { rerender } = render(view(c))
    fireEvent.click(screen.getByRole('button', { name: 'Yes region' }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes db' }))
    await waitFor(() => expect(notify).toHaveBeenCalledTimes(2))
    rerender(view({ ...c, streaming: false }))
    await waitFor(() => expect(c.send).toHaveBeenCalledWith('yes_region', []))
    rerender(view({ ...c, streaming: true }))
    rerender(view({ ...c, streaming: false }))
    await waitFor(() => expect(c.send).toHaveBeenCalledWith('yes_db', []))
  })
})

// What the person picked or typed stands only for the card they saw.
describe('a card that changes under an answer', () => {
  const destructive = ['ui.surface', { surfaceId: 'files', provenance: 'agent', message: v({ updateComponents: { surfaceId: 'files', components: [
    text('yes-l', 'Delete the files'), { id: 'yes', component: 'Button', child: 'yes-l', action: { event: { name: 'delete_all', context: {} } } }] } }) }]
  const two = [card('files'), card('db')]
  const after = [...two, destructive]

  it('drops the pick, says so, and sends nothing until picked again', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'answer' })
    const { rerender } = render(view(live(two)))
    fireEvent.click(screen.getByRole('button', { name: 'Yes files' }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes db' }))
    expect(sendAll()).toBeEnabled()
    rerender(view(live(after)))
    expect(screen.getByRole('button', { name: 'Delete the files' })).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByText(/This card changed/)).toBeInTheDocument()
    expect(sendAll()).toBeDisabled()
    fireEvent.click(sendAll())
    expect(api.chatUIAction).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Delete the files' }))
    fireEvent.click(sendAll())
    await waitFor(() => expect(api.chatUIAction).toHaveBeenCalledTimes(1))
    expect(api.chatUIAction.mock.calls[0][1].actions.map(a => [a.name, a.revision])).toEqual([['delete_all', 't1:3'], ['yes_db', 't1:2']])
  })

  it('drops a pick kept across a reload', () => {
    const first = render(view(live(two)))
    fireEvent.click(screen.getByRole('button', { name: 'Yes files' }))
    first.unmount()
    render(view(live(after)))
    expect(screen.getByRole('button', { name: 'Delete the files' })).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByText(/This card changed/)).toBeInTheDocument()
  })

  it('drops a skip made on the earlier card', () => {
    const { rerender } = render(view(live(two)))
    fireEvent.click(screen.getAllByRole('button', { name: 'Skip this one' })[0])
    rerender(view(live(after)))
    expect(screen.queryByRole('button', { name: /Skipped/ })).toBeNull()
  })

  it('names the version of a card pressed alone', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'answer' })
    render(view(live([card('files')])))
    fireEvent.click(screen.getByRole('button', { name: 'Yes files' }))
    await waitFor(() => expect(api.chatUIAction.mock.calls[0][1].action.revision).toBe('t1:1'))
  })

  const form = (label, bind = '/v', options) => ['ui.surface', { surfaceId: 'f', provenance: 'agent', message: v({ createSurface: {
    surfaceId: 'f', dataModel: { v: options ? [] : '', w: '' }, components: [{ id: 'root', component: 'Column', children: ['in', 'go'] },
      options ? { id: 'in', component: 'ChoicePicker', variant: 'mutuallyExclusive', value: { '@path': bind }, options }
        : { id: 'in', component: 'TextField', label, value: { '@path': bind } },
      ...button('go', 'Save', 'save', { v: { '@path': '/v' } })] } }) }]
  const change = (...msg) => ['ui.surface', { surfaceId: 'f', provenance: 'agent', message: v({ updateComponents: { surfaceId: 'f', components: msg } }) }]

  it('clears a typed value when its field is relabelled or rebound, but not on a progress update', () => {
    const evs = [form('Folder to keep')]
    const { rerender } = render(view(live(evs)))
    const box = name => screen.getByRole('textbox', { name })
    fireEvent.change(box('Folder to keep'), { target: { value: 'photos' } })
    const progress = ['ui.surface', { surfaceId: 'f', provenance: 'agent', message: v({ updateDataModel: { surfaceId: 'f', path: '/w', value: '50%' } }) }]
    rerender(view(live([...evs, progress])))
    expect(box('Folder to keep')).toHaveValue('photos')
    rerender(view(live([...evs, progress, change({ id: 'in', component: 'TextField', label: 'Folder to DELETE', value: { '@path': '/v' } })])))
    expect(box('Folder to DELETE')).toHaveValue('')
    expect(screen.getByText(/This card changed/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  })

  it('clears a choice when the options are relabelled', () => {
    const opts = label => [{ label, value: 'a' }]
    const evs = [form('', '/v', opts('Keep'))]
    const { rerender } = render(view(live(evs)))
    fireEvent.click(screen.getByRole('radio', { name: 'Keep' }))
    rerender(view(live([...evs, change({ id: 'in', component: 'ChoicePicker', variant: 'mutuallyExclusive', value: { '@path': '/v' }, options: opts('Delete') })])))
    expect(screen.getByRole('radio', { name: 'Delete' })).not.toBeChecked()
  })

  it('clears a chat block pick when the block is not the one picked on', async () => {
    const block = label => ['assistant.delta', { partId: 'p1', text: '```choices\n' + JSON.stringify({ question: 'Files?', options: [{ id: 'a', label }, { id: 'b', label: 'Leave' }] }) + '\n```\n```choices\n{"question":"Db?","options":[{"id":"x","label":"pg"},{"id":"y","label":"my"}]}\n```\n' }]
    const first = render(view(finished([block('Keep')])))
    fireEvent.click(await screen.findByRole('button', { name: 'Keep' }))
    first.unmount()
    render(view(finished([block('Delete')])))
    expect(await screen.findByRole('button', { name: 'Delete' })).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByText(/This question changed/)).toBeInTheDocument()
  })
})

describe('what a next message can hold', () => {
  const fence = o => '```choices\n' + JSON.stringify(o) + '\n```\n'
  const block = ['assistant.delta', { partId: 'p1', text: fence({ question: 'Branch', options: [{ id: 'a', label: 'main' }, { id: 'b', label: 'dev' }] }) }]

  it('tells the host how much the blocks add to the cards', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'next_turn', message: 'card' })
    render(view(finished([block, card('region')])))
    fireEvent.click(await screen.findByRole('button', { name: 'main' }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes region' }))
    fireEvent.click(sendAll())
    await waitFor(() => expect(api.chatUIAction).toHaveBeenCalled())
    expect(api.chatUIAction.mock.calls[0][1].appendBytes).toBe('Branch: main'.length)
  })

  const turn = (id, c) => ({ role: 'turn', turnId: id, state: turnState(id, [card(c), done]) })
  const echo = () => api.chatUIAction.mockImplementation((_, env) => Promise.resolve({ outcome: 'accepted', delivered: 'next_turn', message: env.action.name }))

  it('sends a press again when the chat did not take it', async () => {
    echo()
    // null: another send was starting.
    const send = vi.fn().mockResolvedValueOnce(null).mockResolvedValue(true)
    render(view(mk({ messages: [turn('t1', 'region')], send })))
    fireEvent.click(screen.getByRole('button', { name: 'Yes region' }))
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2), { timeout: 4000 })
    expect(send.mock.calls[1][0]).toBe('yes_region')
  })

  it('keeps a queued message the chat did not take', async () => {
    echo()
    const send = vi.fn().mockResolvedValueOnce(null).mockResolvedValue(true)
    const c = mk({ messages: [turn('t1', 'region')], send, streaming: true, activeTurnId: 't3', liveTurn: turnState('t3', []) })
    const { rerender } = render(view(c))
    fireEvent.click(screen.getByRole('button', { name: 'Yes region' }))
    await waitFor(() => expect(notify).toHaveBeenCalled())
    rerender(view({ ...c, streaming: false }))
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2), { timeout: 4000 })
    expect(send.mock.calls[1][0]).toBe('yes_region')
  })
})

describe('several chat blocks', () => {
  const fence = (t, o) => '```' + t + '\n' + JSON.stringify(o) + '\n```\n'
  const choices = q => fence('choices', { question: q, options: [{ id: 'a', label: `${q} A` }, { id: 'b', label: `${q} B` }] })
  const form = fence('form', { title: 'Run', fields: [{ id: 'n', type: 'text', label: 'Name' }] })
  const said = md => ['assistant.delta', { partId: 'p1', text: md }]

  it('picks without sending, then sends one message', async () => {
    const c = finished([said(choices('Branch') + choices('Region') + form)])
    render(view(c))
    fireEvent.click(await screen.findByRole('button', { name: 'Branch A' }))
    expect(c.send).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Region B' }))
    expect(sendAll()).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Submit' })).toBeNull()
    fireEvent.change(screen.getByRole('textbox', { name: 'Name' }), { target: { value: 'nightly' } })
    expect(c.send).not.toHaveBeenCalled()
    fireEvent.click(sendAll())
    await waitFor(() => expect(c.send).toHaveBeenCalledTimes(1))
    expect(c.send).toHaveBeenCalledWith('Branch: Branch A\n\nRegion: Region B\n\nRun\nName: nightly', [])
    await waitFor(() => expect(screen.getByRole('button', { name: 'Region A' })).toBeDisabled())
    expect(screen.queryByTestId('answer-set-bar')).toBeNull()
  })

  it('sends one block at once, as before', async () => {
    const c = finished([said(choices('Branch'))])
    render(view(c))
    fireEvent.click(await screen.findByRole('button', { name: 'Branch A' }))
    await waitFor(() => expect(c.send).toHaveBeenCalledWith('Branch: Branch A', []))
  })

  it('sends a card and a block of one reply together', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'next_turn', message: 'card press' })
    const c = finished([said(choices('Branch')), card('region')])
    render(view(c))
    fireEvent.click(await screen.findByRole('button', { name: 'Branch A' }))
    fireEvent.click(screen.getByRole('button', { name: 'Yes region' }))
    expect(api.chatUIAction).not.toHaveBeenCalled()
    expect(c.send).not.toHaveBeenCalled()
    fireEvent.click(sendAll())
    await waitFor(() => expect(c.send).toHaveBeenCalledWith('card press\n\nBranch: Branch A', []))
    expect(c.send).toHaveBeenCalledTimes(1)
  })
})

// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'

// The entry screen and the start form of the open-ended chat mode. The CLI decides (availability, the charter, the
// phrase); the form shows what the person must read and asks for the phrase they write from it.
const getChat = vi.fn(), onChat = vi.fn()
const { openStartApi } = vi.hoisted(() => ({ openStartApi: { entry: vi.fn(), render: vi.fn(), start: vi.fn(), roster: vi.fn() } }))
vi.mock('../../lib/openStartApi.js', () => ({ openStartApi }))

import { OpenStartForm } from './OpenStartForm.jsx'

const HASH = 'ab12cd34' + 'e'.repeat(56)
const ENTRY = { mode_available: true, run_exists: false, platform: 'linux', not_defended: 'What this mode does NOT defend against:\n  - It adds no isolation.' }
const PREVIEW = { start_token: 'tok-1', hash: HASH, lines: ['spending', 'host_settings'], lift_count: 2, text: `CHARTER ${HASH}\nGoal: ship it`, phrase_template: 'I understand: lift <N> lines, charter <first 8 characters of the hash>' }
const TYPED = 'I understand: lift 2 lines, charter ab12cd34'

beforeEach(() => {
  vi.clearAllMocks()
  getChat.mockReset(); getChat.mockReturnValue({ id: 'chat-7' }); onChat.mockReset()
  openStartApi.entry.mockResolvedValue(ENTRY)
  openStartApi.roster.mockResolvedValue({ error: 'no roster' })
  openStartApi.render.mockResolvedValue(PREVIEW)
  openStartApi.start.mockResolvedValue({ ok: true, started: true, output: '{"run_id":"p/1"}', detail: '', code: '' })
})
afterEach(cleanup)

const type = (label, value) => fireEvent.change(screen.getByLabelText(label), { target: { value } })

async function fillForm({ budget = 'unlimited' } = {}) {
  type('Goal', 'ship it')
  fireEvent.click(screen.getByRole('button', { name: 'Add a command criterion' }))
  type('Criterion 1 command', 'go test ./...')
  type('Criterion 1 expected result', 'ok')
  fireEvent.click(screen.getByLabelText('Spending'))
  fireEvent.click(screen.getByLabelText('Host settings'))
  type('Boss provider', 'anthropic'); type('Boss model', 'opus'); type('Boss version', '4')
  type('Verifier provider', 'anthropic'); type('Verifier model', 'sonnet'); type('Verifier version', '4')
  type('Judging policy', 'default')
  if (budget) fireEvent.click(screen.getByLabelText(budget === 'fixed' ? 'A fixed budget' : 'Unlimited (no ceiling of its own)'))
  if (budget === 'fixed') type('Budget in USD', '5')
}

describe('the entry screen', () => {
  it('says there is no human approval, that typing is steering, where Stop is, and what this machine does not defend against', async () => {
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    expect(await screen.findByText(/There is no human approval during the run/)).toBeInTheDocument()
    expect(screen.getByText(/You can steer by typing in this chat; typing is steering for the boss, never an answer to a boss question/)).toBeInTheDocument()
    expect(screen.getByText(/Stop is in the chat header/)).toBeInTheDocument()
    expect(screen.getByTestId('open-not-defended')).toHaveTextContent('does NOT defend against')
    expect(screen.getByTestId('open-platform')).toHaveTextContent('linux')
  })
  it('names the reason and offers nothing to review when the mode cannot start here', async () => {
    openStartApi.entry.mockResolvedValue({ ...ENTRY, mode_available: false, reason: 'containment unavailable: cgroup v2 delegation to the user: not writable' })
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    expect(await screen.findByTestId('open-unavailable')).toHaveTextContent('cgroup v2 delegation to the user')
    expect(screen.queryByRole('button', { name: 'Review' })).toBeNull()
  })
  it('does not offer a form when the probe could not be read', async () => {
    openStartApi.entry.mockResolvedValue({ error: 'the CLI is not here' })
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    expect(await screen.findByTestId('open-unavailable')).toHaveTextContent('the CLI is not here')
  })
})

describe('the form', () => {
  it('has no budget chosen at first and will not review until one is, then maps unlimited and fixed to the charter', async () => {
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    expect(screen.getByLabelText('A fixed budget')).not.toBeChecked()
    expect(screen.getByLabelText('Unlimited (no ceiling of its own)')).not.toBeChecked()
    await fillForm({ budget: '' })
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(await screen.findByText(/Choose unlimited or a fixed budget/)).toBeInTheDocument()
    expect(openStartApi.render).not.toHaveBeenCalled()
    fireEvent.click(screen.getByLabelText('A fixed budget'))
    type('Budget in USD', '5')
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await waitFor(() => expect(openStartApi.render).toHaveBeenCalledTimes(1))
    expect(openStartApi.render.mock.calls[0][0].limits.budget_micro).toBe(5000000)
    expect(openStartApi.render.mock.calls[0][0].limits.no_ceiling).toBeUndefined()
  })
  it('lists the seven lifted lines by name, the platform caps by name only', async () => {
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    for (const name of ['Spending', 'Destructive local actions', 'Secrets and the vault', 'Outbound to third parties', 'Installing software', 'Host settings', 'Platform caps']) {
      expect(screen.getByLabelText(name)).not.toBeChecked()
    }
    expect(screen.getByTestId('open-line-platform_caps')).toHaveTextContent(/by name only/)
  })
  it('asks for at least one success criterion', async () => {
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    type('Goal', 'x')
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(await screen.findByText(/at least one success criterion/)).toBeInTheDocument()
  })
})

describe('the review and the typed phrase', () => {
  async function toReview(props = { getChat, onChat }, opts) {
    render(<OpenStartForm {...props} />)
    await screen.findByLabelText('Goal')
    await fillForm(opts)
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    return screen.findByTestId('open-review')
  }
  it('shows the full hash, every lifted line and the phrase template, and never the phrase itself', async () => {
    const review = await toReview()
    expect(review).toHaveTextContent(HASH)
    expect(review).toHaveTextContent('spending')
    expect(review).toHaveTextContent('host_settings')
    expect(review).toHaveTextContent('I understand: lift <N> lines, charter <first 8 characters of the hash>')
    expect(document.body.textContent).not.toContain(TYPED)
    expect(screen.getByLabelText('Type the phrase')).toHaveValue('')
  })
  it('starts only with a typed phrase, sends the phrase as typed with the chat the CLI makes the boss behind, and shows the answer', async () => {
    await toReview()
    const start = screen.getByRole('button', { name: 'Start the run' })
    expect(start).toBeDisabled()
    type('Type the phrase', TYPED)
    expect(start).toBeEnabled()
    fireEvent.click(start)
    await waitFor(() => expect(openStartApi.start).toHaveBeenCalledTimes(1))
    const [charter, phrase, token, chat] = openStartApi.start.mock.calls[0]
    expect([phrase, token, chat, charter.goal, charter.lines]).toEqual([TYPED, 'tok-1', { id: 'chat-7' }, 'ship it', ['spending', 'host_settings']])
    expect(await screen.findByTestId('open-started')).toBeInTheDocument()
  })
  it('shows the CLI\'s refusal when the phrase is wrong and does not look started', async () => {
    openStartApi.start.mockResolvedValue({ ok: false, started: false, output: '', detail: 'the phrase does not match; nothing started', code: 'invalid_input' })
    await toReview()
    type('Type the phrase', 'yes')
    fireEvent.click(screen.getByRole('button', { name: 'Start the run' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('the phrase does not match; nothing started')
    expect(screen.queryByTestId('open-started')).toBeNull()
  })
  it('cannot start without a way to get its chat, and says so', async () => {
    await toReview({ getChat: null })
    type('Type the phrase', TYPED)
    expect(screen.getByRole('button', { name: 'Start the run' })).toBeDisabled()
    expect(screen.getByTestId('open-no-boss')).toBeInTheDocument()
    expect(openStartApi.start).not.toHaveBeenCalled()
  })
  it('shows the CLI\'s words when the preview is refused', async () => {
    openStartApi.render.mockResolvedValue({ error: 'the charter is invalid: the goal is empty', code: 'invalid_input' })
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    await fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('the goal is empty')
    expect(screen.queryByTestId('open-review')).toBeNull()
  })
})

describe('after the start', () => {
  async function toStart(props = { getChat, onChat }) {
    render(<OpenStartForm {...props} />)
    await screen.findByLabelText('Goal')
    await fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await screen.findByTestId('open-review')
    type('Type the phrase', TYPED)
    fireEvent.click(screen.getByRole('button', { name: 'Start the run' }))
  }
  it('cannot be submitted again, tells the nav to look, and calls onStarted with the CLI\'s words', async () => {
    const onStarted = vi.fn(); const seen = vi.fn()
    window.addEventListener('monoagent:controlroom-changed', seen)
    render(<OpenStartForm getChat={getChat} onChat={onChat} onStarted={onStarted} />)
    await screen.findByLabelText('Goal')
    await fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await screen.findByTestId('open-review')
    type('Type the phrase', TYPED)
    fireEvent.click(screen.getByRole('button', { name: 'Start the run' }))
    expect(await screen.findByTestId('open-started')).toHaveTextContent('p/1')
    expect(screen.getByRole('button', { name: 'Start the run' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Review' })).toBeDisabled()
    expect(openStartApi.start).toHaveBeenCalledTimes(1)
    expect(onStarted).toHaveBeenCalledTimes(1)
    expect(seen).toHaveBeenCalled()
    window.removeEventListener('monoagent:controlroom-changed', seen)
  })
  it('says the run may have started when the outcome is unknown, and offers no second start', async () => {
    openStartApi.start.mockResolvedValue({ ok: false, started: false, state: 'unknown', detail: 'the run may have started: check the Control room and press Stop', code: 'unknown' })
    await toStart()
    expect(await screen.findByTestId('open-unknown')).toHaveTextContent('may have started')
    expect(screen.getByRole('button', { name: 'Start the run' })).toBeDisabled()
  })
  it('shows busy and the native dialog refusals in the CLI\'s words and allows a new preview', async () => {
    openStartApi.start.mockResolvedValue({ ok: false, started: false, state: 'not_started', detail: 'no native confirmation dialog is available here', code: 'no_native_dialog' })
    await toStart()
    expect(await screen.findByRole('alert')).toHaveTextContent('no native confirmation dialog')
    expect(screen.queryByTestId('open-started')).toBeNull()
  })
})

describe('accessibility', () => {
  it('gives each Remove button its own name, and links an error to its field', async () => {
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    fireEvent.click(screen.getByRole('button', { name: 'Add a command criterion' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add a rubric criterion' }))
    expect(screen.getByRole('button', { name: 'Remove criterion 1' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Remove criterion 2' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    const err = await screen.findByText(/Say what the run is for/)
    expect(screen.getByLabelText('Goal')).toHaveAttribute('aria-describedby', err.id)
    expect(screen.getByLabelText('Goal')).toHaveAttribute('aria-invalid', 'true')
  })
  it('moves focus to the review when it appears', async () => {
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    await fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    const review = await screen.findByTestId('open-review')
    await waitFor(() => expect(review).toHaveFocus())
  })
})

describe('the goal counter and a failed call', () => {
  it('shows how many of 2000 characters the goal has and refuses more', async () => {
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    type('Goal', 'x'.repeat(2001))
    expect(screen.getByTestId('open-goal-count')).toHaveTextContent('2001/2000')
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(await screen.findByText(/at most 2000 characters/)).toBeInTheDocument()
    expect(openStartApi.render).not.toHaveBeenCalled()
  })
  it('shows the unknown state, not a plain error, when the start call itself throws', async () => {
    openStartApi.start.mockRejectedValue(new Error('bridge closed'))
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    await fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await screen.findByTestId('open-review')
    type('Type the phrase', TYPED)
    fireEvent.click(screen.getByRole('button', { name: 'Start the run' }))
    expect(await screen.findByTestId('open-unknown')).toHaveTextContent('may have started')
    expect(screen.getByRole('button', { name: 'Start the run' })).toBeDisabled()
  })
})

describe('the started view and a second review', () => {
  it('shows only the CLI\'s words for a started run, not its raw document', async () => {
    openStartApi.start.mockResolvedValue({ ok: true, started: true, state: 'started', output: '{"run_id":"p/1","charter_id":"c","hash":"deadbeef","generation":1,"anchor_bound":false}', detail: '', code: '' })
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    await fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await screen.findByTestId('open-review')
    type('Type the phrase', TYPED)
    fireEvent.click(screen.getByRole('button', { name: 'Start the run' }))
    const started = await screen.findByTestId('open-started')
    expect(started).toHaveTextContent('p/1')
    expect(started.textContent).not.toMatch(/[{}"]|deadbeef/)
  })
  it('a second review starts clean: the old refusal is gone and the new review has focus', async () => {
    openStartApi.start.mockResolvedValue({ ok: false, started: false, state: 'not_started', detail: 'the confirmation was declined; nothing started', code: 'declined' })
    openStartApi.render.mockResolvedValueOnce({ ...PREVIEW, start_token: 'tok-1' }).mockResolvedValueOnce({ ...PREVIEW, start_token: 'tok-2' })
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    await fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await screen.findByTestId('open-review')
    type('Type the phrase', TYPED)
    fireEvent.click(screen.getByRole('button', { name: 'Start the run' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('declined')
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await waitFor(() => expect(openStartApi.render).toHaveBeenCalledTimes(2))
    const review = await screen.findByTestId('open-review')
    await waitFor(() => expect(review).toHaveFocus())
    expect(screen.queryByText(/declined/)).toBeNull()
    expect(screen.getByLabelText('Type the phrase')).toHaveValue('')
  })
})

describe('after an unknown outcome', () => {
  it('warns that a run may exist on the next review', async () => {
    openStartApi.start.mockResolvedValue({ ok: false, started: false, state: 'unknown', detail: 'the run may have started', code: 'unknown' })
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    await fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await screen.findByTestId('open-review')
    type('Type the phrase', TYPED)
    fireEvent.click(screen.getByRole('button', { name: 'Start the run' }))
    await screen.findByTestId('open-unknown')
    expect(await screen.findByTestId('open-may-exist')).toHaveTextContent('may already exist')
  })
})

describe('the chat of the start and the boss\'s first turn', () => {
  async function toStart() {
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    await fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await screen.findByTestId('open-review')
    type('Type the phrase', TYPED)
    fireEvent.click(screen.getByRole('button', { name: 'Start the run' }))
  }
  it('hands the chat the person is in to the preview and to the start, and a chat the start made to onChat', async () => {
    getChat.mockReturnValue({ spec: { runtime: 'claude', access: 'full' } })
    openStartApi.start.mockResolvedValue({ ok: false, started: false, state: 'not_started', detail: 'the entry is refused', code: 'invalid_input', chat: { id: 'made-1' } })
    await toStart()
    await screen.findByRole('alert')
    expect(openStartApi.render.mock.calls[0][1]).toEqual({ spec: { runtime: 'claude', access: 'full' } })
    expect(openStartApi.start.mock.calls[0][3]).toEqual({ spec: { runtime: 'claude', access: 'full' } })
    expect(onChat).toHaveBeenCalledWith({ id: 'made-1' })
  })
  it('says the boss has no first turn, never silently, and keeps the run shown as started', async () => {
    openStartApi.start.mockResolvedValue({ ok: true, started: true, output: '{"run_id":"p/1","first_turn":"failed","first_turn_error":"host not running"}', detail: '', code: '' })
    await toStart()
    expect(await screen.findByTestId('open-started')).toHaveTextContent('p/1')
    expect(screen.getByTestId('open-no-first-turn')).toHaveTextContent('the boss has no first turn')
  })
  it('shows no first-turn warning when the turn was queued', async () => {
    openStartApi.start.mockResolvedValue({ ok: true, started: true, output: '{"run_id":"p/1","first_turn":"queued"}', detail: '', code: '' })
    await toStart()
    await screen.findByTestId('open-started')
    expect(screen.queryByTestId('open-no-first-turn')).toBeNull()
  })
})

const ROSTER = { runtimes: [
  { runtime: 'claude', installed: true, version: '2.1', models: [{ model: 'sonnet', state: 'ready' }, { model: 'opus', state: 'ready' }] },
  { runtime: 'codex', installed: true, version: '0.9', models: [{ model: 'gpt', state: 'ready' }] },
] }

describe('the lists of the form', () => {
  it('fill the boss and a different verifier from the roster, as lists', async () => {
    openStartApi.roster.mockResolvedValue(ROSTER)
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    await waitFor(() => expect(screen.getByLabelText('Boss provider')).toHaveValue('claude'))
    expect(screen.getByLabelText('Boss provider').tagName).toBe('SELECT')
    expect(screen.getByLabelText('Boss model')).toHaveValue('sonnet')
    expect(screen.getByLabelText('Boss version')).toHaveValue('2.1')
    const verifier = ['Verifier provider', 'Verifier model'].map(l => screen.getByLabelText(l).value).join('/')
    expect(verifier).not.toBe('claude/sonnet')
    expect(screen.getByLabelText('Judging policy')).toHaveValue('default')
    expect(screen.getByLabelText('Judging policy').tagName).toBe('SELECT')
  })
  it('follow the provider: its models only, and a chosen model keeps the pin whole', async () => {
    openStartApi.roster.mockResolvedValue(ROSTER)
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await waitFor(() => expect(screen.getByLabelText('Boss provider')).toHaveValue('claude'))
    expect([...screen.getByLabelText('Boss model').options].map(o => o.value)).toEqual(['sonnet', 'opus'])
    fireEvent.change(screen.getByLabelText('Boss provider'), { target: { value: 'codex' } })
    expect(screen.getByLabelText('Boss model')).toHaveValue('gpt')
    expect(screen.getByLabelText('Boss version')).toHaveValue('0.9')
  })
  it('never overwrite a pin the person chose before the roster came', async () => {
    let resolve
    openStartApi.roster.mockReturnValue(new Promise(r => { resolve = r }))
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    type('Boss provider', 'mine'); type('Boss model', 'm'); type('Boss version', '1')
    resolve(ROSTER)
    await new Promise(r => setTimeout(r, 20))
    expect(screen.getByLabelText('Boss provider')).toHaveValue('mine')
  })
  it('start a charter with the defaults alone: only the goal, a criterion and the budget are written', async () => {
    openStartApi.roster.mockResolvedValue(ROSTER)
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await waitFor(() => expect(screen.getByLabelText('Boss provider')).toHaveValue('claude'))
    type('Goal', 'ship it')
    fireEvent.click(screen.getByRole('button', { name: 'Add a rubric criterion' }))
    type('Criterion 1 rubric', 'it works')
    fireEvent.click(screen.getByLabelText('Unlimited (no ceiling of its own)'))
    fireEvent.click(screen.getByLabelText('Let the boss approve small asks by itself'))
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await waitFor(() => expect(openStartApi.render).toHaveBeenCalled())
    const charter = openStartApi.render.mock.calls[0][0]
    expect(charter.pins.judging_policy).toBe('default')
    expect(charter.pins.boss.provider).toBe('claude')
    expect(charter.per_ask.ask_tasks).toBe(5)
  })
  it('make the per-ask limits lists', async () => {
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    fireEvent.click(screen.getByLabelText('Let the boss approve small asks by itself'))
    for (const l of ['Per-ask spend (USD)', 'Per-ask tasks', 'Per-ask depth', 'Run cap spend (USD)', 'Run cap tasks']) expect(screen.getByLabelText(l).tagName).toBe('SELECT')
  })
  it('keep the typed pin fields when the roster is empty or unreadable, and say how to start', async () => {
    render(<OpenStartForm getChat={getChat} onChat={onChat} />)
    await screen.findByLabelText('Goal')
    expect(screen.getByLabelText('Boss provider').tagName).toBe('INPUT')
    expect(screen.getByTestId('open-howto')).toHaveTextContent(/press Review/)
  })
})

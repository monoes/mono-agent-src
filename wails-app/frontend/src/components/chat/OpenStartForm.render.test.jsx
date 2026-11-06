// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'

// The entry screen and the start form of the open-ended chat mode. The CLI decides (availability, the charter, the
// phrase); the form shows what the person must read and asks for the phrase they write from it.
const { openStartApi } = vi.hoisted(() => ({ openStartApi: { entry: vi.fn(), render: vi.fn(), start: vi.fn() } }))
vi.mock('../../lib/openStartApi.js', () => ({ openStartApi }))

import { OpenStartForm } from './OpenStartForm.jsx'

const HASH = 'ab12cd34' + 'e'.repeat(56)
const ENTRY = { mode_available: true, run_exists: false, platform: 'linux', not_defended: 'What this mode does NOT defend against:\n  - It adds no isolation.' }
const PREVIEW = { hash: HASH, lines: ['spending', 'host_settings'], lift_count: 2, text: `CHARTER ${HASH}\nGoal: ship it`, phrase_template: 'I understand: lift <N> lines, charter <first 8 characters of the hash>' }
const TYPED = 'I understand: lift 2 lines, charter ab12cd34'

beforeEach(() => {
  vi.clearAllMocks()
  openStartApi.entry.mockResolvedValue(ENTRY)
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
    render(<OpenStartForm bossSession={7} />)
    expect(await screen.findByText(/There is no human approval during the run/)).toBeInTheDocument()
    expect(screen.getByText(/You can steer by typing in this chat; typing is steering for the boss, never an answer to a boss question/)).toBeInTheDocument()
    expect(screen.getByText(/Stop is in the chat header/)).toBeInTheDocument()
    expect(screen.getByTestId('open-not-defended')).toHaveTextContent('does NOT defend against')
    expect(screen.getByTestId('open-platform')).toHaveTextContent('linux')
  })
  it('names the reason and offers nothing to review when the mode cannot start here', async () => {
    openStartApi.entry.mockResolvedValue({ ...ENTRY, mode_available: false, reason: 'containment unavailable: cgroup v2 delegation to the user: not writable' })
    render(<OpenStartForm bossSession={7} />)
    expect(await screen.findByTestId('open-unavailable')).toHaveTextContent('cgroup v2 delegation to the user')
    expect(screen.queryByRole('button', { name: 'Review' })).toBeNull()
  })
  it('does not offer a form when the probe could not be read', async () => {
    openStartApi.entry.mockResolvedValue({ error: 'the CLI is not here' })
    render(<OpenStartForm bossSession={7} />)
    expect(await screen.findByTestId('open-unavailable')).toHaveTextContent('the CLI is not here')
  })
})

describe('the form', () => {
  it('has no budget chosen at first and will not review until one is, then maps unlimited and fixed to the charter', async () => {
    render(<OpenStartForm bossSession={7} />)
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
    render(<OpenStartForm bossSession={7} />)
    await screen.findByLabelText('Goal')
    for (const name of ['Spending', 'Destructive local actions', 'Secrets and the vault', 'Outbound to third parties', 'Installing software', 'Host settings', 'Platform caps']) {
      expect(screen.getByLabelText(name)).not.toBeChecked()
    }
    expect(screen.getByTestId('open-line-platform_caps')).toHaveTextContent(/by name only/)
  })
  it('asks for at least one success criterion', async () => {
    render(<OpenStartForm bossSession={7} />)
    await screen.findByLabelText('Goal')
    type('Goal', 'x')
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(await screen.findByText(/at least one success criterion/)).toBeInTheDocument()
  })
})

describe('the review and the typed phrase', () => {
  async function toReview(props = { bossSession: 7 }, opts) {
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
  it('starts only with a typed phrase, sends the phrase as typed with the boss session, and shows the answer', async () => {
    await toReview()
    const start = screen.getByRole('button', { name: 'Start the run' })
    expect(start).toBeDisabled()
    type('Type the phrase', TYPED)
    expect(start).toBeEnabled()
    fireEvent.click(start)
    await waitFor(() => expect(openStartApi.start).toHaveBeenCalledTimes(1))
    const [charter, phrase, boss] = openStartApi.start.mock.calls[0]
    expect([phrase, boss, charter.goal, charter.lines]).toEqual([TYPED, 7, 'ship it', ['spending', 'host_settings']])
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
  it('cannot start without the boss session the chat creates, and says so', async () => {
    await toReview({ bossSession: null })
    type('Type the phrase', TYPED)
    expect(screen.getByRole('button', { name: 'Start the run' })).toBeDisabled()
    expect(screen.getByTestId('open-no-boss')).toBeInTheDocument()
    expect(openStartApi.start).not.toHaveBeenCalled()
  })
  it('shows the CLI\'s words when the preview is refused', async () => {
    openStartApi.render.mockResolvedValue({ error: 'the charter is invalid: the goal is empty', code: 'invalid_input' })
    render(<OpenStartForm bossSession={7} />)
    await screen.findByLabelText('Goal')
    await fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('the goal is empty')
    expect(screen.queryByTestId('open-review')).toBeNull()
  })
})

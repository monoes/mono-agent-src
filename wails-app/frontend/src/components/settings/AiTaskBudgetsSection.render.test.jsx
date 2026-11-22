// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, within, cleanup } from '@testing-library/react'
import AiTaskBudgetsSection from './AiTaskBudgetsSection.jsx'
import { aitaskBudgetApi } from './aitaskBudgetApi.js'
import { confirm } from '../ConfirmDialog.jsx'
import '../../i18n.js'

vi.mock('../ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))

vi.mock('./aitaskBudgetApi.js', async (orig) => ({
  ...(await orig()),
  aitaskBudgetApi: { list: vi.fn(), set: vi.fn(), clear: vi.fn(), forTask: vi.fn() },
}))

// The `monoagentcli aitask budget show --json` document.
const doc = (caps = []) => ({ operatorUsd: 5, spentUsd: 1.5, caps })
const cap = (scope, key, capUsd, usedUsd = 0) => ({ scope, key, capUsd, usedUsd, updatedBy: 'operator', updatedAt: 'x' })

beforeEach(() => {
  aitaskBudgetApi.list.mockResolvedValue(doc())
  aitaskBudgetApi.set.mockResolvedValue({})
  aitaskBudgetApi.clear.mockResolvedValue({})
  confirm.mockResolvedValue(true)
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

const capRow = (id) => document.querySelector(`[data-cap="${id}"]`)

describe('AiTaskBudgetsSection', () => {
  it('shows the global, per-kind and per-agent caps with what each has used, and the coder ceiling', async () => {
    aitaskBudgetApi.list.mockResolvedValue(doc([
      cap('global', '', 10, 1.5), cap('kind', 'generic', 2, 0.25), cap('conversation', 'conv-1', 1, 1), cap('role', 'writer', 0),
    ]))
    render(<AiTaskBudgetsSection />)
    expect(await screen.findByText('$1.50 of $10.00')).toBeInTheDocument()
    expect(within(capRow('kind:generic')).getByText('$0.2500 of $2.00')).toBeInTheDocument()
    expect(within(capRow('conversation:conv-1')).getByText('Conversation conv-1')).toBeInTheDocument()
    expect(within(capRow('role:writer')).getByText('Role writer')).toBeInTheDocument()
    // A cap of 0 says what it means.
    expect(within(capRow('role:writer')).getByText('no spend')).toBeInTheDocument()
    expect(screen.getByTestId('aitask-budgets')).toHaveTextContent('the coder budget ($5.00 per message)')
    expect(screen.getByTestId('aitask-budgets')).toHaveTextContent('0 means no spend, never unlimited')
  })

  it('sets the global cap, a kind cap and an agent cap through the CLI and reads them back', async () => {
    render(<AiTaskBudgetsSection />)
    await screen.findByText('No global cap.')
    fireEvent.change(screen.getByLabelText('Global cap in USD'), { target: { value: '7.5' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set cap' }))
    await waitFor(() => expect(aitaskBudgetApi.set).toHaveBeenCalledWith('global', '', 7.5))

    fireEvent.change(screen.getByLabelText('Kind (e.g. generic)'), { target: { value: 'generic' } })
    fireEvent.change(screen.getByLabelText('Kind (e.g. generic) cap in USD'), { target: { value: '0' } })
    fireEvent.click(screen.getAllByRole('button', { name: 'Add cap' })[0])
    await waitFor(() => expect(aitaskBudgetApi.set).toHaveBeenCalledWith('kind', 'generic', 0))

    fireEvent.change(screen.getByLabelText('Kind of agent cap'), { target: { value: 'role' } })
    fireEvent.change(screen.getByLabelText('Conversation id or role'), { target: { value: 'writer' } })
    fireEvent.change(screen.getByLabelText('Conversation id or role cap in USD'), { target: { value: '3' } })
    fireEvent.click(screen.getAllByRole('button', { name: 'Add cap' })[1])
    await waitFor(() => expect(aitaskBudgetApi.set).toHaveBeenCalledWith('role', 'writer', 3))
    // Each save reloads the caps.
    await waitFor(() => expect(aitaskBudgetApi.list).toHaveBeenCalledTimes(4))
  })

  it('does not send a negative or empty amount, or a cap with no key', async () => {
    render(<AiTaskBudgetsSection />)
    await screen.findByText('No global cap.')
    const set = screen.getByRole('button', { name: 'Set cap' })
    expect(set).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Global cap in USD'), { target: { value: '-1' } })
    expect(set).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Kind (e.g. generic) cap in USD'), { target: { value: '2' } })
    expect(screen.getAllByRole('button', { name: 'Add cap' })[0]).toBeDisabled()
    expect(aitaskBudgetApi.set).not.toHaveBeenCalled()
  })

  it('removes a cap, and shows a refusal from the CLI', async () => {
    aitaskBudgetApi.list.mockResolvedValue(doc([cap('kind', 'generic', 2)]))
    render(<AiTaskBudgetsSection />)
    fireEvent.click(await screen.findByRole('button', { name: 'Remove the cap for generic' }))
    await waitFor(() => expect(aitaskBudgetApi.clear).toHaveBeenCalledWith('kind', 'generic'))

    aitaskBudgetApi.set.mockRejectedValue(Object.assign(new Error('only the user sets a budget'), { code: 'operator_only' }))
    fireEvent.change(screen.getByLabelText('Global cap in USD'), { target: { value: '4' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set cap' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('only the user sets a budget')
  })

  it('asks before lifting a cap and keeps it when the answer is no', async () => {
    aitaskBudgetApi.list.mockResolvedValue(doc([cap('kind', 'generic', 2)]))
    confirm.mockResolvedValue(false)
    render(<AiTaskBudgetsSection />)
    fireEvent.click(await screen.findByRole('button', { name: 'Remove the cap for generic' }))
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1))
    expect(confirm.mock.calls[0][0]).toMatch(/generic/)
    expect(confirm.mock.calls[0][1]).toMatchObject({ danger: true })
    expect(aitaskBudgetApi.clear).not.toHaveBeenCalled()
  })

  it('reports a load failure', async () => {
    aitaskBudgetApi.list.mockRejectedValue(new Error('no cli'))
    render(<AiTaskBudgetsSection />)
    expect(await screen.findByRole('alert')).toHaveTextContent('no cli')
  })
})

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, within, cleanup } from '@testing-library/react'
import AgentPurposesSection from './AgentPurposesSection.jsx'
import { agentPurposesApi } from './agentPurposesApi.js'

vi.mock('./agentPurposesApi.js', async (orig) => ({
  ...(await orig()),
  agentPurposesApi: { list: vi.fn(), set: vi.fn(), reset: vi.fn(), resetAll: vi.fn() },
}))

// The `monoagentcli agent purposes list --json` document.
const pair = (runtime, model, state = 'ready') => ({
  runtime, model, label: `${runtime} ${model}`, state, selectable: state === 'ready' || state === 'stale',
})
const purpose = (id, over = {}) => ({
  id, title: id[0].toUpperCase() + id.slice(1), description: `${id} work`, categories: [], configured: false,
  pairs: [], rule: 'any', effort: '', different_from_author: false, budget_share_pct: 0, fallback: false, ...over,
})
const AVAILABLE = [pair('claude', 'opus'), pair('codex', 'gpt', 'stale'), pair('gemini', 'pro', 'failed'), pair('grok', 'x', 'untested')]
const doc = (purposes) => ({ purposes, available: AVAILABLE })

beforeEach(() => {
  agentPurposesApi.list.mockResolvedValue(doc([purpose('coding'), purpose('reviewing')]))
  agentPurposesApi.set.mockResolvedValue({})
  agentPurposesApi.reset.mockResolvedValue({ reset: [] })
  agentPurposesApi.resetAll.mockResolvedValue({ reset: [] })
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

const open = async (id) => {
  const row = await waitFor(() => { const r = document.querySelector(`[data-purpose="${id}"]`); expect(r).toBeTruthy(); return r })
  fireEvent.click(within(row).getAllByRole('button')[0])
  return row
}

describe('AgentPurposesSection', () => {
  it('lists the purposes and says a purpose with nothing chosen uses every validated pair', async () => {
    agentPurposesApi.list.mockResolvedValue(doc([purpose('coding'), purpose('reviewing', { configured: true, rule: 'order', pairs: [pair('claude', 'opus')] })]))
    render(<AgentPurposesSection />)
    const coding = await open('coding')
    expect(within(coding).getByText('all validated pairs')).toBeInTheDocument()
    expect(screen.getByTestId('agent-purposes')).toHaveTextContent('Only you change this')
    expect(document.querySelector('[data-purpose="reviewing"]')).toHaveTextContent('1 chosen · order')
  })

  it('offers only selectable pairs and names the others with their state', async () => {
    render(<AgentPurposesSection />)
    const row = await open('coding')
    expect(row.querySelector('[data-offer="claude/opus"]')).toBeTruthy()
    expect(row.querySelector('[data-offer="codex/gpt"]')).toBeTruthy()
    expect(row.querySelector('[data-offer="gemini/pro"]')).toBeNull()
    expect(row.querySelector('[data-offer="grok/x"]')).toBeNull()
    expect(row).toHaveTextContent('gemini pro (failed)')
    expect(row).toHaveTextContent('(untested)')
  })

  it('saves the whole state of the purpose as runtime/model pairs in order', async () => {
    render(<AgentPurposesSection />)
    const row = await open('coding')
    fireEvent.click(within(row.querySelector('[data-offer="claude/opus"]')).getByRole('checkbox'))
    fireEvent.click(within(row.querySelector('[data-offer="codex/gpt"]')).getByRole('checkbox'))
    fireEvent.change(within(row).getByLabelText('Selection rule'), { target: { value: 'order' } })
    fireEvent.click(within(row).getByRole('button', { name: 'Move codex gpt up' }))
    fireEvent.change(within(row).getByLabelText('Effort'), { target: { value: ' high ' } })
    fireEvent.change(within(row).getByLabelText('Budget share (%)'), { target: { value: '25' } })
    fireEvent.click(within(row).getByLabelText('Use a different model than the one that wrote the work'))
    fireEvent.click(within(row).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(agentPurposesApi.set).toHaveBeenCalledWith('coding', {
      pairs: ['codex/gpt', 'claude/opus'], rule: 'order', effort: 'high', differentModel: true, budgetSharePct: 25, fallback: false,
    }))
    expect(agentPurposesApi.list).toHaveBeenCalledTimes(2)
  })

  it('does not let a chosen pair that can no longer run be saved, until it is removed', async () => {
    agentPurposesApi.list.mockResolvedValue(doc([purpose('coding', { configured: true, pairs: [pair('claude', 'opus'), pair('gemini', 'pro', 'failed')] })]))
    render(<AgentPurposesSection />)
    const row = await open('coding')
    expect(row.querySelector('[data-pair="gemini/pro"]')).toHaveAttribute('data-state', 'failed')
    fireEvent.change(within(row).getByLabelText('Effort'), { target: { value: 'low' } })
    expect(within(row).getByRole('alert')).toHaveTextContent('gemini pro cannot be used now')
    expect(within(row).getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.click(within(row).getByRole('button', { name: 'Remove gemini pro' }))
    fireEvent.click(within(row).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(agentPurposesApi.set).toHaveBeenCalledWith('coding', expect.objectContaining({ pairs: ['claude/opus'], effort: 'low' })))
  })

  it('refuses a budget share that is not a whole number from 0 to 100', async () => {
    render(<AgentPurposesSection />)
    const row = await open('coding')
    fireEvent.change(within(row).getByLabelText('Budget share (%)'), { target: { value: '101' } })
    expect(within(row).getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.change(within(row).getByLabelText('Budget share (%)'), { target: { value: '1e2' } })
    expect(within(row).getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('resets one purpose, or all, and shows a CLI refusal', async () => {
    agentPurposesApi.list.mockResolvedValue(doc([purpose('coding', { configured: true, pairs: [pair('claude', 'opus')] })]))
    render(<AgentPurposesSection />)
    const row = await open('coding')
    fireEvent.click(within(row).getByRole('button', { name: 'Reset to default' }))
    await waitFor(() => expect(agentPurposesApi.reset).toHaveBeenCalledWith('coding'))
    fireEvent.click(screen.getByRole('button', { name: 'Reset all to default' }))
    await waitFor(() => expect(agentPurposesApi.resetAll).toHaveBeenCalled())

    const e = new Error('only the operator sets purposes'); e.code = 'operator_only'
    agentPurposesApi.set.mockRejectedValue(e)
    fireEvent.change(within(row).getByLabelText('Effort'), { target: { value: 'low' } })
    fireEvent.click(within(row).getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('only the operator sets purposes')).toBeInTheDocument()
  })

  it('shows a load failure instead of an empty pane', async () => {
    agentPurposesApi.list.mockRejectedValue(new Error('not signed in'))
    render(<AgentPurposesSection />)
    expect(await screen.findByRole('alert')).toHaveTextContent('not signed in')
  })
})

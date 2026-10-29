// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, within, cleanup } from '@testing-library/react'
import i18n from '../../i18n.js'
import AgentPurposesSection from './AgentPurposesSection.jsx'
import contract from './__fixtures__/agentPurposes.contract.json'

vi.mock('../ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))
import { confirm } from '../ConfirmDialog.jsx'

const App = {}
const reply = (v) => Promise.resolve(JSON.stringify(v))
const clone = () => JSON.parse(JSON.stringify(contract))

beforeEach(async () => {
  await i18n.changeLanguage('en')
  for (const k of ['AgentPurposesList', 'AgentPurposeSet', 'AgentPurposeReset']) App[k] = vi.fn()
  App.AgentPurposesList.mockImplementation(() => reply(contract))
  App.AgentPurposeSet.mockImplementation(() => reply({ ok: true }))
  App.AgentPurposeReset.mockImplementation(() => reply({ ok: true }))
  confirm.mockResolvedValue(true)
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); vi.clearAllMocks(); delete window.go })

const row = (id) => document.querySelector(`[data-purpose="${id}"]`)
async function open(id) {
  await screen.findByTestId('agent-purposes')
  const li = await waitFor(() => { const r = row(id); if (!r) throw new Error('no row'); return r })
  fireEvent.click(within(li).getAllByRole('button')[0])
  return li
}

describe('AgentPurposesSection', () => {
  it('lists the purposes; a default one says it uses every validated model', async () => {
    render(<AgentPurposesSection />)
    await waitFor(() => expect(document.querySelectorAll('[data-purpose]')).toHaveLength(18))
    expect(within(row('writing')).getByTestId('purpose-summary')).toHaveTextContent('Uses every validated model')
    expect(within(row('review')).getByTestId('purpose-summary')).toHaveTextContent('2 models chosen')
    expect(within(row('writing')).getByRole('button', { name: /Writing and docs/ })).toHaveAttribute('aria-expanded', 'false')
    // A collapsed row still says when a chosen model is not usable.
    expect(within(row('triage')).getByTestId('purpose-summary')).toHaveTextContent('2 models chosen · 2 not usable')
    expect(within(row('review')).getByTestId('purpose-summary')).not.toHaveTextContent('not usable')
  })

  it('shows a loading state, then a load error with a Retry that reloads', async () => {
    App.AgentPurposesList.mockImplementationOnce(() => reply({ error: 'monoagentcli not found' }))
    render(<AgentPurposesSection />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load the purposes: monoagentcli not found')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(document.querySelectorAll('[data-purpose]')).toHaveLength(18))
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('refuses a reply that is not the contract', async () => {
    App.AgentPurposesList.mockImplementation(() => reply({ nope: 1 }))
    render(<AgentPurposesSection />)
    expect(await screen.findByRole('alert')).toHaveTextContent('unexpected reply')
  })

  it('a default purpose: nothing chosen, Save and Reset are off', async () => {
    render(<AgentPurposesSection />)
    const li = await open('writing')
    expect(within(li).getByText('Nothing chosen: this purpose uses every validated model.')).toBeInTheDocument()
    expect(within(li).getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(within(li).getByRole('button', { name: 'Reset' })).toBeDisabled()
  })

  it('a chosen purpose shows its chips in order with their status, rule, effort, switch and share', async () => {
    render(<AgentPurposesSection />)
    const li = await open('review')
    const chips = [...li.querySelectorAll('[data-pair]')]
    expect(chips.map(c => c.dataset.pair)).toEqual(['codex:gpt-5', 'claude:opus'])
    expect(within(chips[0]).getByTestId('chip')).toHaveAttribute('data-status', 'ok')
    expect(within(li).getByLabelText('Selection rule')).toHaveValue('order')
    expect(within(li).getByLabelText('Effort')).toHaveValue('high')
    expect(within(li).getByLabelText('If none of them is available')).toHaveValue('none')
    expect(within(li).getByLabelText('Budget share (%)')).toHaveValue('15')
    expect(within(li).getByRole('switch', { name: 'A different model than the author' })).toHaveAttribute('aria-checked', 'true')
    expect(within(li).getByRole('button', { name: 'Reset' })).toBeEnabled()
  })

  it('a stale and a failed pair stay listed with their chip and a warning, and can be removed', async () => {
    render(<AgentPurposesSection />)
    const li = await open('triage')
    const stale = li.querySelector('[data-pair="claude:haiku"]')
    const failed = li.querySelector('[data-pair="codex:gpt-5-mini"]')
    expect(within(stale).getByTestId('chip')).toHaveTextContent('stale')
    expect(within(failed).getByTestId('chip')).toHaveTextContent('failed')
    expect(within(li).getByText(/not validated stays listed/)).toBeInTheDocument()
    fireEvent.click(within(stale).getByRole('button', { name: 'Remove claude / haiku' }))
    expect(li.querySelector('[data-pair="claude:haiku"]')).toBeNull()
    expect(within(li).getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('the Add model list offers only ok pairs; the others show their status and are disabled', async () => {
    render(<AgentPurposesSection />)
    const li = await open('writing')
    const select = within(li).getByLabelText('Add a model')
    const opt = (text) => [...select.querySelectorAll('option')].find(o => o.textContent.startsWith(text))
    expect(opt('opus')).not.toBeDisabled()
    expect(opt('gpt-5')).not.toBeDisabled()
    expect(opt('haiku')).toBeDisabled()
    expect(opt('haiku').textContent).toBe('haiku (stale)')
    expect(opt('gpt-5-mini')).toBeDisabled()
    expect(opt('gpt-5-mini').textContent).toBe('gpt-5-mini (failed)')
    expect(opt('llama3:8b')).toBeDisabled()
    expect(opt('llama3:8b').textContent).toBe('llama3:8b (unvalidated)')
    expect([...select.querySelectorAll('optgroup')].map(g => g.label)).toEqual(['claude', 'codex', 'ollama', 'opencode'])
    // Even a forced change to an unvalidated pair adds nothing.
    fireEvent.change(select, { target: { value: 'ollama:llama3:8b' } })
    expect(li.querySelector('[data-pair]')).toBeNull()
    fireEvent.change(select, { target: { value: 'claude:opus' } })
    expect(li.querySelector('[data-pair="claude:opus"]')).not.toBeNull()
    expect(within(li).getByLabelText('Add a model').querySelector('option[value="claude:opus"]')).toBeDisabled()
  })

  it('saves the whole purpose with one call and reads the document back', async () => {
    render(<AgentPurposesSection />)
    const li = await open('writing')
    fireEvent.change(within(li).getByLabelText('Add a model'), { target: { value: 'claude:opus' } })
    fireEvent.change(within(li).getByLabelText('Add a model'), { target: { value: 'codex:gpt-5' } })
    fireEvent.change(within(li).getByLabelText('Selection rule'), { target: { value: 'order' } })
    fireEvent.change(within(li).getByLabelText('Effort'), { target: { value: 'low' } })
    fireEvent.change(within(li).getByLabelText('Budget share (%)'), { target: { value: '25' } })
    fireEvent.click(within(li).getByRole('switch'))
    fireEvent.click(within(li).getByRole('button', { name: 'Move claude / opus down' }))
    expect(within(row('writing')).getByText('unsaved')).toBeInTheDocument()
    fireEvent.click(within(li).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(App.AgentPurposeSet).toHaveBeenCalledWith('writing', ['codex:gpt-5', 'claude:opus'], 'order', 'low', true, 'any', 25))
    await waitFor(() => expect(App.AgentPurposesList).toHaveBeenCalledTimes(2))
  })

  it('shows Saving while the call is pending and keeps the controls off', async () => {
    let done
    App.AgentPurposeSet.mockImplementation(() => new Promise(r => { done = () => r(JSON.stringify({ ok: true })) }))
    render(<AgentPurposesSection />)
    const li = await open('review')
    fireEvent.change(within(li).getByLabelText('Selection rule'), { target: { value: 'any' } })
    fireEvent.click(within(li).getByRole('button', { name: 'Save' }))
    expect(await within(li).findByRole('button', { name: 'Saving…' })).toBeDisabled()
    expect(within(li).getByLabelText('Selection rule')).toBeDisabled()
    done()
    await waitFor(() => expect(within(li).getByRole('button', { name: 'Save' })).toBeInTheDocument())
  })

  it('a failed save shows the error inline, keeps the draft and Retry saves again', async () => {
    App.AgentPurposeSet.mockImplementationOnce(() => reply({ error: 'database is locked', code: 'store_busy' }))
    render(<AgentPurposesSection />)
    const li = await open('review')
    fireEvent.change(within(li).getByLabelText('Selection rule'), { target: { value: 'cheapest' } })
    fireEvent.click(within(li).getByRole('button', { name: 'Save' }))
    expect(await within(li).findByTestId('purpose-error')).toHaveTextContent('database is locked')
    expect(within(li).getByLabelText('Selection rule')).toHaveValue('cheapest')
    fireEvent.click(within(li).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(App.AgentPurposeSet).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(within(li).queryByTestId('purpose-error')).toBeNull())
  })

  it('an operator-only refusal is shown with its message and no Retry', async () => {
    App.AgentPurposeSet.mockImplementation(() => reply({ error: 'only the operator edits purposes', code: 'operator_only' }))
    render(<AgentPurposesSection />)
    const li = await open('review')
    fireEvent.click(within(li).getByRole('switch'))
    fireEvent.click(within(li).getByRole('button', { name: 'Save' }))
    expect(await within(li).findByTestId('purpose-error')).toHaveTextContent('only the operator edits purposes')
    expect(within(li).queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('a bad budget share blocks Save and says why', async () => {
    render(<AgentPurposesSection />)
    const li = await open('writing')
    fireEvent.change(within(li).getByLabelText('Budget share (%)'), { target: { value: '150' } })
    expect(within(li).getByText('Enter a value from 0 to 100.')).toBeInTheDocument()
    expect(within(li).getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.change(within(li).getByLabelText('Budget share (%)'), { target: { value: 'abc' } })
    expect(within(li).getByText('Enter a number, for example 20.')).toBeInTheDocument()
  })

  it('Discard puts the saved values back', async () => {
    render(<AgentPurposesSection />)
    const li = await open('review')
    fireEvent.change(within(li).getByLabelText('Selection rule'), { target: { value: 'any' } })
    fireEvent.click(within(li).getByRole('button', { name: 'Discard changes' }))
    expect(within(li).getByLabelText('Selection rule')).toHaveValue('order')
  })

  it('Reset asks first; cancelling does nothing, confirming resets and reloads', async () => {
    render(<AgentPurposesSection />)
    const li = await open('review')
    confirm.mockResolvedValueOnce(false)
    fireEvent.click(within(li).getByRole('button', { name: 'Reset' }))
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1))
    expect(confirm.mock.calls[0][0]).toContain('Code review')
    expect(App.AgentPurposeReset).not.toHaveBeenCalled()
    fireEvent.click(within(li).getByRole('button', { name: 'Reset' }))
    await waitFor(() => expect(App.AgentPurposeReset).toHaveBeenCalledWith('review'))
    await waitFor(() => expect(App.AgentPurposesList).toHaveBeenCalledTimes(2))
  })

  it('after a reset the card follows the CLI copy', async () => {
    render(<AgentPurposesSection />)
    const li = await open('review')
    const after = clone()
    Object.assign(after.purposes.find(p => p.id === 'review'), { pairs: [], rule: 'any', effort: '', differentFromAuthor: false, fallback: 'any', budgetSharePct: 0, isDefault: true })
    App.AgentPurposesList.mockImplementation(() => reply(after))
    fireEvent.click(within(li).getByRole('button', { name: 'Reset' }))
    await waitFor(() => expect(within(row('review')).getByTestId('purpose-summary')).toHaveTextContent('Uses every validated model'))
    expect(within(row('review')).getByLabelText('Selection rule')).toHaveValue('any')
  })

  it('with an empty roster nothing can be added and the pane says so', async () => {
    const d = clone(); d.roster = []; d.purposes = d.purposes.map(p => ({ ...p, pairs: [], isDefault: true }))
    App.AgentPurposesList.mockImplementation(() => reply(d))
    render(<AgentPurposesSection />)
    expect(await screen.findByTestId('purposes-empty-roster')).toBeInTheDocument()
    const li = await open('writing')
    expect(within(li).getByLabelText('Add a model')).toBeDisabled()
  })

  it('renders in Spanish', async () => {
    await i18n.changeLanguage('es')
    render(<AgentPurposesSection />)
    await waitFor(() => expect(document.querySelectorAll('[data-purpose]')).toHaveLength(18))
    expect(within(row('writing')).getByTestId('purpose-summary')).toHaveTextContent('Usa todos los modelos validados')
    expect(within(row('writing')).getByRole('button', { name: /Redacción y documentación/ })).toBeInTheDocument()
  })

  it('shows names as text, never as markup', async () => {
    const d = clone(); d.purposes[0].title = '<img src=x onerror=alert(1)>'; d.purposes[0].id = 'zzz'
    App.AgentPurposesList.mockImplementation(() => reply(d))
    render(<AgentPurposesSection />)
    const li = await waitFor(() => { const r = row('zzz'); if (!r) throw new Error('x'); return r })
    expect(li.querySelector('img')).toBeNull()
    expect(li).toHaveTextContent('<img src=x onerror=alert(1)>')
  })
})

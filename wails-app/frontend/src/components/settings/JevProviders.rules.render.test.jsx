// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../../i18n.js'
import { sampledMax, sampledCeiling, capabilityError } from './jevShared.jsx'

// The decision-provider rules the GUI mirrors from the CLI: probability
// kinds, the default-switch and replace consents, keyless only on this
// computer, capability checks, pinned surfaces, the default's error and the
// sampled-threshold warning. Fake bindings as JevProviders.render.test.jsx.
const App = {}
const caps = (probability, kinds = ['choice', 'noul', 'score'], extra = {}) =>
  ({ kinds, probability, max_choice_options: 20, questions_per_request: 1, models: true, ...extra })
const TYPESAFE = { name: 'typesafe', kind: 'typesafe', base_url: 'https://api.typesafe.ai', model: 'jev-latest', key_set: true, builtin: true, caps: caps('native') }
const LOCAL = { name: 'local', kind: 'openai', base_url: 'http://localhost:11434/v1', model: 'llama3.1', key_set: false, builtin: false, caps: caps('logprob') }
const ROUTER = { name: 'router', kind: 'openai', base_url: 'https://openrouter.ai/api/v1', model: 'm', key_set: true, builtin: false, caps: caps('sampled') }
const HF = { name: 'hf', kind: 'hf-zeroshot', base_url: 'https://hf.example.com/models/x', model: '', key_set: true, builtin: false,
  caps: caps('softmax', ['choice', 'noul'], { max_choice_options: 0, options_per_request: 0 }) }
const providers = (def = 'typesafe', list = [TYPESAFE, LOCAL, ROUTER, HF]) => ({ default: def, kinds: ['openai', 'anthropic', 'hf-zeroshot'], providers: list })

const surfaces = (ov = {}) => [
  { surface: 'hil', title: 'Human-in-Loop suggestions', description: 'd', egress: ["the item's field values"], enabled: true, threshold: 0.9, default_threshold: 0.9, ...ov.hil },
  { surface: 'inbox', title: 'Inbox triage', description: 'd', egress: ['message subject and body'], enabled: true, threshold: 0.7, default_threshold: 0.7, ...ov.inbox },
]
const status = (ov, extra = {}) => ({
  profile_id: 'default', key_source: 'vault', key_entry: 'typesafe', model: 'jev-latest', base_url: 'https://api.typesafe.ai',
  provider: 'typesafe', provider_kind: 'typesafe', provider_host: 'api.typesafe.ai', probability: 'native', surfaces: surfaces(ov), ...extra,
})

beforeEach(() => {
  for (const k of ['JevStatus', 'JevSetKey', 'JevTestKey', 'JevRemoveKey', 'JevSetSurface', 'JevUsage',
    'JevProviders', 'JevAddProvider', 'JevUseProvider', 'JevProbeProvider', 'JevRemoveProvider']) App[k] = vi.fn()
  App.JevStatus.mockResolvedValue(status())
  App.JevUsage.mockResolvedValue({ surfaces: [], total: { surface: 'total', calls: 0 } })
  App.JevProviders.mockResolvedValue(providers())
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

async function mount(props = {}) {
  const { default: JevSection } = await import('./JevSection.jsx')
  render(<JevSection {...props} />)
  await screen.findByTestId('jev-providers')
  await waitFor(() => expect(document.querySelector('[data-jev-provider="local"]')).toBeInTheDocument())
}
async function mountProfile() {
  const { default: JevSection } = await import('./JevSection.jsx')
  render(<JevSection part="profile" />)
  await waitFor(() => expect(within(srow('hil')).getByRole('combobox')).toBeInTheDocument())
}
const prow = (n) => document.querySelector(`[data-jev-provider="${n}"]`)
const srow = (s) => document.querySelector(`[data-jev-surface="${s}"]`)

describe('probabilities', () => {
  it('names softmax and sampled, and says what a sampled number can reach', async () => {
    await mount()
    expect(within(prow('hf')).getByText('Forced split across the options')).toBeInTheDocument()
    expect(within(prow('hf')).getByTestId('jev-prob-note-hf')).toHaveTextContent('A forced split across the candidate labels; not calibrated.')
    expect(within(prow('router')).getByText('Estimated from 5 answers')).toBeInTheDocument()
    expect(within(prow('router')).getByTestId('jev-prob-note-router')).toHaveTextContent(
      'Smoothed estimates from 5 answers: at most about 0.86 on a two-option question (0.75 on three), so on its own this provider cannot settle anything at a 0.9 threshold.')
    expect(within(prow('local')).queryByTestId('jev-prob-note-local')).not.toBeInTheDocument()
  })

  it('shows the kinds and limits, never a limit of 0 as "0 options"', async () => {
    await mount()
    expect(within(prow('local')).getByText('Answers choice, noul, score · up to 20 options per question')).toBeInTheDocument()
    expect(within(prow('hf')).getByText('Answers choice, noul')).toBeInTheDocument()
    expect(prow('hf')).not.toHaveTextContent(/0 options/)
  })

  it('the ceilings are the add-one smoothed shares of 5 samples', () => {
    expect(sampledMax(2)).toBeCloseTo(6 / 7, 12)
    expect(sampledMax(3)).toBe(0.75)
    expect(sampledCeiling('hil')).toBe(0.75)
    expect(sampledCeiling('people_links')).toBeCloseTo(6 / 7, 12)
    expect(sampledCeiling('inbox')).toBeCloseTo(6 / 7, 12)
  })
})

describe('default switch', () => {
  it('lists org deciders, workflow nodes and dynamic-org chats besides the features', async () => {
    await mount()
    fireEvent.click(within(prow('router')).getByRole('button', { name: 'Use router as default' }))
    const box = screen.getByTestId('jev-provider-switch-confirm')
    expect(within(box).getByText('Human-in-Loop suggestions')).toBeInTheDocument()
    expect(within(box).getByTestId('jev-switch-deciders')).toHaveTextContent('Org deciders set to Jev without their own provider, in every profile:')
    expect(within(box).getByTestId('jev-switch-deciders')).toHaveTextContent("the org's name and goal, the operator policy and earlier decisions")
    expect(within(box).getByTestId('jev-switch-nodes')).toHaveTextContent(
      'Every workflow node that uses a decision model without naming a provider (ai.choose, ai.decide, browser.jev, Human-in-Loop auto_decide, applications.evaluate with runtime jev)')
    expect(within(box).getByTestId('jev-switch-dynorg')).toHaveTextContent('Dynamic-org coder chats that ask Jev to staff their workers (surface dynorg)')
  })

  it('asks when switching back to TypeSafe from a default on this computer', async () => {
    App.JevStatus.mockResolvedValue(status({}, { provider: 'local' }))
    App.JevProviders.mockResolvedValue(providers('local'))
    await mount()
    fireEvent.click(within(prow('typesafe')).getByRole('button', { name: 'Use typesafe as default' }))
    const box = screen.getByTestId('jev-provider-switch-confirm')
    expect(box).toHaveTextContent('Its host is api.typesafe.ai, not this computer.')
    expect(App.JevUseProvider).not.toHaveBeenCalled()
    App.JevUseProvider.mockResolvedValue({ scope: 'default', provider: 'typesafe' })
    fireEvent.click(within(box).getByRole('button', { name: 'Send to api.typesafe.ai and switch' }))
    await waitFor(() => expect(App.JevUseProvider).toHaveBeenCalledWith('typesafe', ''))
  })

  it('a feature pinned to the default does not follow it, so it is not listed', async () => {
    App.JevStatus.mockResolvedValue(status({ hil: { provider: 'typesafe', provider_pinned: true } }))
    await mount()
    fireEvent.click(within(prow('router')).getByRole('button', { name: 'Use router as default' }))
    const box = screen.getByTestId('jev-provider-switch-confirm')
    expect(within(box).queryByText('Human-in-Loop suggestions')).not.toBeInTheDocument()
    expect(within(box).getByText('Inbox triage')).toBeInTheDocument()
  })

  it("shows the default provider's error in the Decision model block", async () => {
    App.JevStatus.mockResolvedValue(status({}, { provider: 'router', provider_error: 'jev: provider "router" has no key in vault entry "decision-router"' }))
    App.JevProviders.mockResolvedValue(providers('router'))
    await mount()
    expect(screen.getByTestId('jev-provider-error')).toHaveTextContent(
      'The default provider router cannot be used: provider "router" has no key in vault entry "decision-router"')
    expect(screen.getByTestId('jev-provider-error')).toHaveAttribute('role', 'alert')
  })
})

describe('add form', () => {
  const open = async () => {
    await mount()
    fireEvent.click(screen.getByRole('button', { name: /Add provider/ }))
    return screen.getByTestId('jev-add-provider')
  }
  const fill = (form, f) => {
    for (const [l, v] of Object.entries(f)) fireEvent.change(within(form).getByLabelText(l), { target: { value: v } })
  }

  it('asks before replacing a saved provider on another host, then passes replace', async () => {
    const form = await open()
    fireEvent.change(within(form).getByLabelText('Preset'), { target: { value: 'custom' } })
    fill(form, { Kind: 'openai', Name: 'router', 'Base URL': 'https://llm.example.com/v1', 'API key': 'k' })
    fireEvent.click(within(form).getByRole('button', { name: 'Add provider' }))
    const box = within(form).getByTestId('jev-add-replace-confirm')
    expect(box).toHaveTextContent('Replace provider router: its host changes from openrouter.ai to llm.example.com. ' +
      'From now on its key and the data of everything that uses it go to llm.example.com.')
    expect(App.JevAddProvider).not.toHaveBeenCalled()
    fireEvent.click(within(box).getByRole('button', { name: 'Cancel' }))
    expect(within(form).queryByTestId('jev-add-replace-confirm')).not.toBeInTheDocument()
    expect(App.JevAddProvider).not.toHaveBeenCalled()
    fireEvent.click(within(form).getByRole('button', { name: 'Add provider' }))
    App.JevAddProvider.mockResolvedValue({ ...ROUTER, base_url: 'https://llm.example.com/v1' })
    fireEvent.click(within(within(form).getByTestId('jev-add-replace-confirm')).getByRole('button', { name: 'Replace router' }))
    await waitFor(() => expect(App.JevAddProvider).toHaveBeenCalledWith(expect.objectContaining({
      name: 'router', base_url: 'https://llm.example.com/v1', api_key: 'k', replace: true })))
  })

  it('asks too when the host stays, and a new provider name does not ask', async () => {
    const form = await open()
    fill(form, { Name: 'local' })
    fireEvent.click(within(form).getByRole('button', { name: 'Add provider' }))
    expect(within(form).getByTestId('jev-add-replace-confirm')).toHaveTextContent(
      "Replace provider local? This form's kind, base URL, model and key replace its own for everything that uses it; it still sends to localhost:11434.")
    fill(form, { Name: 'ollama2' })
    expect(within(form).queryByTestId('jev-add-replace-confirm')).not.toBeInTheDocument()
    App.JevAddProvider.mockResolvedValue({ ...LOCAL, name: 'ollama2' })
    fireEvent.click(within(form).getByRole('button', { name: 'Add provider' }))
    await waitFor(() => expect(App.JevAddProvider).toHaveBeenCalledWith(expect.objectContaining({ name: 'ollama2', replace: false })))
  })

  it('offers keyless only for a URL on this computer', async () => {
    const form = await open()
    expect(within(form).getByLabelText('API key (optional)')).toBeInTheDocument()
    fireEvent.change(within(form).getByLabelText('Preset'), { target: { value: 'custom' } })
    fill(form, { Kind: 'openai', Name: 'box', 'Base URL': 'https://gpu.example.com/v1' })
    const add = within(form).getByRole('button', { name: 'Add provider' })
    expect(add).toBeDisabled()
    const key = within(form).getByLabelText('API key')
    expect(key).toHaveAttribute('aria-invalid', 'true')
    expect(within(form).getByText(/A provider on another host needs a key\./)).toBeInTheDocument()
    fill(form, { 'Base URL': 'http://127.0.0.1:8080/v1' })
    expect(within(form).getByLabelText('API key (optional)')).toBeInTheDocument()
    expect(add).toBeEnabled()
    // The hosted zero-shot preset is on another host too.
    fireEvent.change(within(form).getByLabelText('Preset'), { target: { value: 'hf' } })
    fill(form, { 'Base URL': 'https://hf.example.com/models/y', Name: 'hf2' })
    expect(add).toBeDisabled()
    // The CLI's own refusal still shows inline.
    fill(form, { 'API key': 'k' })
    App.JevAddProvider.mockRejectedValue(new Error('jev: a provider on another host needs a key: provider "hf2" at https://hf.example.com has none'))
    fireEvent.click(add)
    expect(await within(form).findByRole('alert')).toHaveTextContent('a provider on another host needs a key')
  })
})

describe('per-feature provider', () => {
  it('disables a provider that does not answer a kind the feature asks', async () => {
    await mountProfile()
    const hil = within(srow('hil')).getByLabelText('Decision model for Human-in-Loop suggestions')
    const opt = within(hil).getByRole('option', { name: 'hf (does not answer score)' })
    expect(opt).toBeDisabled()
    expect(within(hil).getByRole('option', { name: 'router' })).toBeEnabled()
    // Inbox asks choice and noul, which hf answers.
    expect(within(within(srow('inbox')).getByRole('combobox')).getByRole('option', { name: 'hf' })).toBeEnabled()
  })

  it("renders the CLI's capability refusal by the select, announced", async () => {
    const unprobed = { ...LOCAL, name: 'fresh', caps: caps('', []) }
    App.JevProviders.mockResolvedValue(providers('typesafe', [TYPESAFE, LOCAL, unprobed]))
    await mountProfile()
    const live = within(srow('inbox')).getByTestId('jev-provider-error-inbox')
    expect(live).toHaveAttribute('aria-live', 'assertive')
    App.JevUseProvider.mockRejectedValue(new Error('invalid input: jev: provider "fresh" does not answer noul questions, which surface inbox asks ' +
      '(it answers choice; `jev provider probe fresh` checks again)'))
    fireEvent.change(within(srow('inbox')).getByRole('combobox'), { target: { value: 'fresh' } })
    const alert = await within(live).findByRole('alert')
    expect(alert).toHaveTextContent('fresh does not answer noul questions, which inbox asks (it answers choice). Choose another provider, or test fresh again if it changed.')
    expect(within(srow('inbox')).getByRole('combobox')).toHaveAttribute('aria-describedby', alert.id)
  })

  it('says "Pinned to X" and follows the default again on request', async () => {
    App.JevStatus.mockResolvedValue(status({ hil: { provider: 'typesafe', provider_pinned: true } }))
    await mountProfile()
    expect(within(srow('hil')).getByTestId('jev-pinned-hil')).toHaveTextContent('Pinned to typesafe')
    expect(within(srow('hil')).getByRole('combobox')).toHaveValue('typesafe')
    expect(within(srow('inbox')).queryByTestId('jev-pinned-inbox')).not.toBeInTheDocument()
    expect(within(srow('inbox')).getByRole('combobox')).toHaveValue('')
    App.JevUseProvider.mockResolvedValue({ scope: 'surface', surface: 'hil', cleared: true })
    fireEvent.click(within(srow('hil')).getByRole('button', { name: 'Human-in-Loop suggestions: follow the default provider (typesafe)' }))
    await waitFor(() => expect(App.JevUseProvider).toHaveBeenCalledWith('', 'hil'))
  })

  it('warns, visibly, when a sampled provider cannot reach the threshold', async () => {
    App.JevStatus.mockResolvedValue(status({ hil: { threshold: 0.8 }, inbox: { threshold: 0.85 } }, { provider: 'router' }))
    App.JevProviders.mockResolvedValue(providers('router'))
    await mountProfile()
    // hil gates a three-option question: at most 0.75.
    expect(within(srow('hil')).getByTestId('jev-sampled-warning-hil')).toHaveTextContent(
      'This provider estimates from 5 answers and cannot reach this threshold (at most 0.75 here); it will only suggest.')
    // inbox at 0.85 is reachable on two options (0.857).
    expect(within(srow('inbox')).queryByTestId('jev-sampled-warning-inbox')).not.toBeInTheDocument()
    fireEvent.change(within(srow('inbox')).getByLabelText('Threshold'), { target: { value: '0.9' } })
    expect(within(srow('inbox')).getByTestId('jev-sampled-warning-inbox')).toHaveTextContent('(at most 0.86 here)')
  })

  it('does not warn for a provider that is not sampled', async () => {
    App.JevStatus.mockResolvedValue(status({}, { provider: 'local' }))
    App.JevProviders.mockResolvedValue(providers('local'))
    await mountProfile()
    expect(document.querySelector('[data-testid^="jev-sampled-warning-"]')).toBeNull()
  })
})

describe('capabilityError', () => {
  it('keeps any other error as it is', () => {
    const t = (k) => k
    expect(capabilityError('jev: the CLI could not open its database', t)).toBe('the CLI could not open its database')
    expect(capabilityError('', t)).toBe('')
  })
})

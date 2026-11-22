// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../../i18n.js'
import { isThisComputer, hostOf } from './jevShared.jsx'

// The Decision model block and the per-feature provider select, against
// fake bindings (window.go.main.App, as the generated App.js calls them).
const App = {}
const caps = (probability) => ({ kinds: ['choice', 'noul'], probability, max_choice_options: 20, questions_per_request: 1, models: true })
const TYPESAFE = { name: 'typesafe', kind: 'typesafe', base_url: 'https://api.typesafe.ai', model: 'jev-latest', key_set: true, builtin: true, caps: caps('native') }
const LOCAL = { name: 'local', kind: 'openai', base_url: 'http://localhost:11434/v1', model: 'llama3.1', key_set: false, builtin: false, caps: caps('logprob') }
const ROUTER = { name: 'router', kind: 'openai', base_url: 'https://openrouter.ai/api/v1', model: 'openai/gpt-4o-mini', key_set: true, builtin: false, caps: caps('sampled') }
const providers = (def = 'typesafe', list = [TYPESAFE, LOCAL, ROUTER]) => ({ default: def, kinds: ['openai', 'anthropic', 'hf-zeroshot'], providers: list })

const surfaces = (ov = {}) => [
  { surface: 'hil', title: 'Human-in-Loop suggestions', description: 'd', egress: ["the item's field values"], enabled: true, threshold: 0.9, default_threshold: 0.9, provider: 'typesafe', ...ov.hil },
  { surface: 'inbox', title: 'Inbox triage', description: 'd', egress: ['message subject and body'], enabled: false, threshold: 0.7, default_threshold: 0.7, provider: 'typesafe', ...ov.inbox },
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
const prow = (n) => document.querySelector(`[data-jev-provider="${n}"]`)
const srow = (s) => document.querySelector(`[data-jev-surface="${s}"]`)

describe('isThisComputer', () => {
  it('follows the CLI loopback rule', () => {
    for (const u of ['http://localhost:11434/v1', 'http://127.0.0.1:8080/v1', 'https://localhost', 'http://[::1]:1234/v1', 'http://127.255.0.1/'])
      expect(isThisComputer(u)).toBe(true)
    for (const u of ['https://api.openai.com/v1', 'http://localhost.evil.com/v1', 'http://user@localhost/v1', 'http://127.0.0.256/',
      'http://127.1/', 'http://10.0.0.2:8000/v1', 'localhost:11434', '', undefined])
      expect(isThisComputer(u)).toBe(false)
    expect(hostOf('http://localhost:11434/v1')).toBe('localhost:11434')
  })
})

describe('Decision model block', () => {
  it('lists each provider with kind, host, model, probability badge, key state and the default', async () => {
    await mount()
    expect(within(prow('typesafe')).getByTestId('jev-provider-default')).toHaveTextContent('Default')
    expect(within(prow('typesafe')).getByText('Calibrated by the provider')).toBeInTheDocument()
    expect(within(prow('local')).getByText('From token probabilities')).toBeInTheDocument()
    expect(within(prow('router')).getByText('Estimated from repeated answers')).toBeInTheDocument()
    expect(within(prow('local')).getByText('localhost:11434 · llama3.1')).toBeInTheDocument()
    expect(within(prow('local')).getByText('No key')).toBeInTheDocument()
    expect(within(prow('router')).getByText('Key set')).toBeInTheDocument()
    expect(within(prow('typesafe')).queryByRole('button', { name: 'Use typesafe as default' })).not.toBeInTheDocument()
    expect(screen.getByText(/is a different bar on another/)).toBeInTheDocument()
    // The TypeSafe key row stays, labelled as that provider's key.
    expect(screen.getByText("TypeSafe provider's API key")).toBeInTheDocument()
    expect(screen.getByLabelText('TypeSafe API key')).toHaveAttribute('type', 'password')
  })

  it('disables Remove for the built-in, the default and a provider a feature uses, and says why', async () => {
    App.JevStatus.mockResolvedValue(status({ inbox: { provider: 'router' } }, { provider: 'local' }))
    App.JevProviders.mockResolvedValue(providers('local'))
    await mount()
    const why = (n) => document.getElementById(within(prow(n)).getByRole('button', { name: `Remove ${n}` }).getAttribute('aria-describedby'))
    expect(within(prow('typesafe')).getByRole('button', { name: 'Remove typesafe' })).toBeDisabled()
    expect(why('typesafe')).toHaveTextContent('Built in, cannot be removed.')
    expect(within(prow('local')).getByRole('button', { name: 'Remove local' })).toBeDisabled()
    expect(why('local')).toHaveTextContent('The default.')
    expect(within(prow('router')).getByRole('button', { name: 'Remove router' })).toBeDisabled()
    expect(why('router')).toHaveTextContent('A feature in this profile uses it.')
  })

  it('removes a provider after an inline confirm and shows a CLI refusal inline', async () => {
    await mount()
    fireEvent.click(within(prow('router')).getByRole('button', { name: 'Remove router' }))
    expect(App.JevRemoveProvider).not.toHaveBeenCalled()
    const box = screen.getByTestId('jev-provider-remove-confirm-router')
    expect(box).toHaveTextContent('Delete provider "router" and its saved key?')
    App.JevRemoveProvider.mockRejectedValue(new Error('router is the provider of the people_review surface in profile work'))
    fireEvent.click(within(box).getByRole('button', { name: 'Delete provider' }))
    await waitFor(() => expect(App.JevRemoveProvider).toHaveBeenCalledWith('router'))
    expect(await screen.findByText(/provider of the people_review surface/)).toHaveAttribute('role', 'alert')
  })

  it('probes a provider and announces latency or the error', async () => {
    await mount()
    const live = screen.getByTestId('jev-probe-local')
    expect(live).toHaveAttribute('aria-live', 'polite')
    expect(live).toBeEmptyDOMElement()
    App.JevProbeProvider.mockResolvedValue({ ok: true, caps: caps('logprob'), latency_ms: 42, error: '' })
    fireEvent.click(within(prow('local')).getByRole('button', { name: 'Test local' }))
    await waitFor(() => expect(live).toHaveTextContent('local works · 42 ms'))
    App.JevProbeProvider.mockResolvedValue({ ok: false, caps: {}, latency_ms: 0, error: 'connection refused' })
    fireEvent.click(within(prow('local')).getByRole('button', { name: 'Test local' }))
    await waitFor(() => expect(live).toHaveTextContent('local test failed: connection refused'))
  })

  it('switches the default to a provider on this computer without asking', async () => {
    await mount()
    App.JevUseProvider.mockResolvedValue({ scope: 'default', surface: '', provider: 'local', cleared: false })
    App.JevProviders.mockResolvedValue(providers('local'))
    fireEvent.click(within(prow('local')).getByRole('button', { name: 'Use local as default' }))
    await waitFor(() => expect(App.JevUseProvider).toHaveBeenCalledWith('local', ''))
    expect(screen.queryByTestId('jev-provider-switch-confirm')).not.toBeInTheDocument()
    await screen.findByText('local is now the default.')
    await waitFor(() => expect(within(prow('local')).getByTestId('jev-provider-default')).toBeInTheDocument())
  })

  it('asks before making a provider on another host the default, listing the features that follow it', async () => {
    await mount()
    fireEvent.click(within(prow('router')).getByRole('button', { name: 'Use router as default' }))
    const box = screen.getByTestId('jev-provider-switch-confirm')
    expect(App.JevUseProvider).not.toHaveBeenCalled()
    expect(box).toHaveTextContent('Its host is openrouter.ai, not this computer.')
    // hil is on and follows the default; inbox is off.
    expect(within(box).getByText('Human-in-Loop suggestions')).toBeInTheDocument()
    expect(within(box).getByText("the item's field values")).toBeInTheDocument()
    expect(within(box).queryByText('Inbox triage')).not.toBeInTheDocument()
    expect(box).toHaveTextContent('other profiles that follow the default will also send to openrouter.ai')
    fireEvent.click(within(box).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByTestId('jev-provider-switch-confirm')).not.toBeInTheDocument()
    expect(App.JevUseProvider).not.toHaveBeenCalled()

    fireEvent.click(within(prow('router')).getByRole('button', { name: 'Use router as default' }))
    App.JevUseProvider.mockResolvedValue({ scope: 'default', surface: '', provider: 'router' })
    fireEvent.click(within(screen.getByTestId('jev-provider-switch-confirm')).getByRole('button', { name: 'Send to openrouter.ai and switch' }))
    await waitFor(() => expect(App.JevUseProvider).toHaveBeenCalledWith('router', ''))
  })

  it('a feature with its own provider is not listed in the default switch', async () => {
    App.JevStatus.mockResolvedValue(status({ hil: { provider: 'local' } }))
    await mount()
    fireEvent.click(within(prow('router')).getByRole('button', { name: 'Use router as default' }))
    expect(screen.getByTestId('jev-provider-switch-confirm')).toHaveTextContent('No feature in this profile that follows the default is on.')
  })
})

describe('Add provider', () => {
  const open = async () => {
    await mount()
    fireEvent.click(screen.getByRole('button', { name: /Add provider/ }))
    return screen.getByTestId('jev-add-provider')
  }

  it('pre-fills from a preset and adds a keyless local provider, showing the probe', async () => {
    const form = await open()
    // Ollama is the first preset shown: kind, URL and a model hint (no value).
    expect(within(form).getByLabelText('Base URL')).toHaveValue('http://localhost:11434/v1')
    expect(within(form).getByLabelText('Kind')).toHaveValue('openai')
    expect(within(form).getByLabelText('Model')).toHaveValue('')
    expect(within(form).getByLabelText('Model')).toHaveAttribute('placeholder', 'The model name the server lists')
    expect(within(form).getByLabelText('API key (optional)')).toHaveAttribute('type', 'password')
    fireEvent.change(within(form).getByLabelText('Preset'), { target: { value: 'lmstudio' } })
    expect(within(form).getByLabelText('Base URL')).toHaveValue('http://localhost:1234/v1')
    expect(within(form).getByLabelText('Name')).toHaveValue('lmstudio')
    fireEvent.change(within(form).getByLabelText('Model'), { target: { value: 'qwen2.5-7b' } })
    App.JevAddProvider.mockResolvedValue({ ...LOCAL, name: 'lmstudio', probe: { ok: true, error: '' } })
    fireEvent.click(within(form).getByRole('button', { name: 'Add provider' }))
    await waitFor(() => expect(App.JevAddProvider).toHaveBeenCalledWith({
      name: 'lmstudio', kind: 'openai', base_url: 'http://localhost:1234/v1', model: 'qwen2.5-7b', api_key: '',
      usd_per_mtok_in: 0, usd_per_mtok_out: 0, no_probe: false,
    }))
    expect(await within(form).findByText('Added lmstudio. It answered the test.')).toBeInTheDocument()
    expect(within(form).getByTestId('jev-add-result')).toHaveAttribute('aria-live', 'polite')
    expect(App.JevProviders).toHaveBeenCalledTimes(2)
  })

  it('needs a key for a hosted preset, sends it once and clears the field', async () => {
    const form = await open()
    fireEvent.change(within(form).getByLabelText('Preset'), { target: { value: 'openrouter' } })
    expect(within(form).getByLabelText('Base URL')).toHaveValue('https://openrouter.ai/api/v1')
    // A hosted preset fills in its model, so the value shown is the one sent.
    expect(within(form).getByLabelText('Model')).toHaveValue('openai/gpt-4o-mini')
    const add =within(form).getByRole('button', { name: 'Add provider' })
    expect(add).toBeDisabled()
    const key = within(form).getByLabelText('API key')
    expect(key).toHaveAttribute('type', 'password')
    fireEvent.change(key, { target: { value: 'fake-router-key' } })
    expect(add).toBeEnabled()
    App.JevAddProvider.mockResolvedValue({ ...ROUTER, probe: { ok: false, error: 'jev: HTTP 401: {"error":{"message":"No auth credentials found"}}' } })
    fireEvent.click(add)
    await waitFor(() => expect(App.JevAddProvider).toHaveBeenCalledWith(expect.objectContaining({ name: 'openrouter', model: 'openai/gpt-4o-mini', api_key: 'fake-router-key' })))
    expect(await within(form).findByText('Added router, but the test failed: HTTP 401 — No auth credentials found')).toBeInTheDocument()
    expect(key).toHaveValue('')
  })

  it('refuses a bad name before calling the CLI and shows CLI errors inline', async () => {
    const form = await open()
    fireEvent.change(within(form).getByLabelText('Name'), { target: { value: 'My Model' } })
    expect(within(form).getByLabelText('Name')).toHaveAttribute('aria-invalid', 'true')
    expect(within(form).getByRole('button', { name: 'Add provider' })).toBeDisabled()
    fireEvent.change(within(form).getByLabelText('Name'), { target: { value: 'gpu-box' } })
    fireEvent.change(within(form).getByLabelText('Base URL'), { target: { value: 'http://10.0.0.2:8000/v1' } })
    App.JevAddProvider.mockRejectedValue(new Error('jev: base URL must be https, or http to this computer'))
    fireEvent.click(within(form).getByRole('button', { name: 'Add provider' }))
    expect(await within(form).findByRole('alert')).toHaveTextContent('base URL must be https')
  })
})

describe('Per-feature provider', () => {
  const mountProfile = async () => {
    const { default: JevSection } = await import('./JevSection.jsx')
    render(<JevSection part="profile" />)
    await waitFor(() => expect(within(srow('hil')).getByRole('combobox')).toBeInTheDocument())
  }

  it('offers Default (<name>) and each provider, and follows the default when cleared', async () => {
    App.JevStatus.mockResolvedValue(status({ inbox: { provider: 'local' } }))
    await mountProfile()
    const sel = within(srow('inbox')).getByLabelText('Decision model for Inbox triage')
    expect(sel).toHaveValue('local')
    expect(within(sel).getAllByRole('option').map(o => o.textContent)).toEqual(['Default (typesafe)', 'typesafe', 'local', 'router'])
    expect(within(srow('hil')).getByLabelText('Decision model for Human-in-Loop suggestions')).toHaveValue('')
    // The feature's data goes to its own provider's host.
    expect(within(srow('inbox')).getByText('What is sent to localhost:11434')).toBeInTheDocument()
    App.JevUseProvider.mockResolvedValue({ scope: 'surface', surface: 'inbox', provider: 'typesafe', cleared: true })
    fireEvent.change(sel, { target: { value: '' } })
    await waitFor(() => expect(App.JevUseProvider).toHaveBeenCalledWith('', 'inbox'))
  })

  it('asks before an enabled feature sends to another host', async () => {
    App.JevStatus.mockResolvedValue(status({ hil: { provider: 'local' } }))
    await mountProfile()
    const sel = within(srow('hil')).getByLabelText('Decision model for Human-in-Loop suggestions')
    fireEvent.change(sel, { target: { value: 'router' } })
    const box = screen.getByTestId('jev-provider-confirm-hil')
    expect(box).toHaveTextContent('Human-in-Loop suggestions will send the following to openrouter.ai:')
    expect(within(box).getByText("the item's field values")).toBeInTheDocument()
    expect(App.JevUseProvider).not.toHaveBeenCalled()
    App.JevUseProvider.mockResolvedValue({ scope: 'surface', surface: 'hil', provider: 'router' })
    fireEvent.click(within(box).getByRole('button', { name: 'Send to openrouter.ai' }))
    await waitFor(() => expect(App.JevUseProvider).toHaveBeenCalledWith('router', 'hil'))
  })

  it('switches a disabled feature, or one moving to this computer, directly', async () => {
    await mountProfile()
    App.JevUseProvider.mockResolvedValue({})
    fireEvent.change(within(srow('inbox')).getByRole('combobox'), { target: { value: 'router' } })
    await waitFor(() => expect(App.JevUseProvider).toHaveBeenCalledWith('router', 'inbox'))
    await waitFor(() => expect(within(srow('hil')).getByRole('combobox')).toBeEnabled())
    fireEvent.change(within(srow('hil')).getByRole('combobox'), { target: { value: 'local' } })
    await waitFor(() => expect(App.JevUseProvider).toHaveBeenCalledWith('local', 'hil'))
    expect(screen.queryByTestId('jev-provider-confirm-hil')).not.toBeInTheDocument()
  })

  it('names the default provider host in the consent and the intro when it is not TypeSafe', async () => {
    App.JevStatus.mockResolvedValue(status({}, { provider: 'router', provider_host: 'openrouter.ai' }))
    App.JevProviders.mockResolvedValue(providers('router'))
    await mountProfile()
    expect(screen.getByText(/lets this profile send data to openrouter\.ai\./)).toBeInTheDocument()
    expect(screen.queryByText(/send data to TypeSafe/)).not.toBeInTheDocument()
    cleanup()
    const { default: JevSection } = await import('./JevSection.jsx')
    render(<JevSection part="machine" />)
    expect(await screen.findByText(/nothing is sent to openrouter\.ai until you turn a feature on in a profile's settings/)).toBeInTheDocument()
  })

  it('shows the key warning only for features TypeSafe answers, and a provider column in usage', async () => {
    App.JevStatus.mockResolvedValue(status({ hil: { provider: 'local' } }, { key_source: 'none' }))
    App.JevUsage.mockResolvedValue({ surfaces: [
      { surface: 'hil', provider: 'local', calls: 3, input_tokens: 300, output_tokens: 3, estimated_usd: 0 },
      { surface: 'hil', provider: 'typesafe', calls: 2, input_tokens: 200, estimated_usd: 0.0001 },
    ], total: { surface: 'total', calls: 5, input_tokens: 500, estimated_usd: 0.0001 } })
    await mountProfile()
    expect(screen.queryByText('Enabled features stay inactive until a key is set.')).not.toBeInTheDocument()
    const table = await screen.findByRole('table')
    expect(within(table).getByText('Provider')).toBeInTheDocument()
    expect(within(table).getByText('local')).toBeInTheDocument()
    expect(within(table).getAllByText('Human-in-Loop suggestions')).toHaveLength(2)
  })
})

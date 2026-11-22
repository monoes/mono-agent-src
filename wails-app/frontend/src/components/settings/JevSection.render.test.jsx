// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../../i18n.js'

// The section imports the generated bindings, which call window.go.main.App.
const App = {}
beforeEach(() => {
  for (const k of ['JevStatus', 'JevSetKey', 'JevTestKey', 'JevRemoveKey', 'JevSetSurface', 'JevUsage',
    'JevProviders', 'JevAddProvider', 'JevUseProvider', 'JevProbeProvider', 'JevRemoveProvider']) App[k] = vi.fn()
  // No provider configured: only the built-in TypeSafe one (D4).
  App.JevProviders.mockResolvedValue({ default: 'typesafe', kinds: ['openai', 'anthropic', 'hf-zeroshot'], providers: [
    { name: 'typesafe', kind: 'typesafe', base_url: 'https://api.typesafe.ai', model: 'jev-latest', key_set: true, builtin: true,
      caps: { kinds: ['choice', 'noul', 'score'], probability: 'native', max_choice_options: 255, questions_per_request: 32, models: true } },
  ] })
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

const surfaces = (overrides = {}) => [
  { surface: 'hil', title: 'Human-in-Loop suggestions', description: 'Suggests approve or reject.',
    egress: ["the item's readonly and editable field values", "the node's auto_decide policy text"],
    enabled: false, threshold: 0.9, default_threshold: 0.9, ...overrides.hil },
  { surface: 'inbox', title: 'Inbox triage', description: 'Sorts incoming messages.',
    egress: ['message subject and body', 'sender name'],
    enabled: false, threshold: 0.7, default_threshold: 0.7, ...overrides.inbox },
]
const status = (key_source, key_entry = '', ov) => ({
  profile_id: 'default', key_source, key_entry, model: 'jev-latest', base_url: 'https://api.typesafe.ai', surfaces: surfaces(ov),
})
const emptyUsage = { profile_id: 'default', since: '', surfaces: [], total: { surface: 'total', calls: 0 } }

async function mount(st) {
  App.JevStatus.mockResolvedValue(st)
  App.JevUsage.mockResolvedValue(emptyUsage)
  const { default: JevSection } = await import('./JevSection.jsx')
  render(<JevSection />)
  await screen.findByTestId('jev-key-chip')
}
const row = (s) => document.querySelector(`[data-jev-surface="${s}"]`)

describe('JevSection', () => {
  it('shows "Not set" with no key, disables Test and hides Remove', async () => {
    await mount(status('none'))
    expect(screen.getByTestId('jev-key-chip')).toHaveTextContent('Not set')
    expect(screen.getByRole('button', { name: 'Test' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Remove' })).not.toBeInTheDocument()
    expect(App.JevUsage).toHaveBeenCalledWith('7d')
    expect(screen.getByText('No Jev calls in the last 7 days.')).toBeInTheDocument()
  })

  it('shows the vault entry name for a vault key', async () => {
    await mount(status('vault', 'Jev Api key'))
    expect(screen.getByTestId('jev-key-chip')).toHaveTextContent('Vault entry "Jev Api key"')
    expect(screen.getByRole('button', { name: 'Remove' })).toBeInTheDocument()
  })

  it('shows a key still in the profile vault as shared-to-be, with Test, no Remove and the adopt command', async () => {
    await mount(status('profile-vault', 'typesafe'))
    expect(screen.getByTestId('jev-key-chip')).toHaveTextContent('Set (this profile, will be shared)')
    expect(screen.getByRole('button', { name: 'Test' })).toBeEnabled()
    expect(screen.queryByRole('button', { name: 'Remove' })).not.toBeInTheDocument()
    expect(screen.getByTestId('jev-adopt-hint')).toHaveTextContent('monoagentcli jev key adopt')
  })

  it('splits into the machine half (key) and the profile half (features, usage)', async () => {
    App.JevStatus.mockResolvedValue(status('vault', 'typesafe'))
    App.JevUsage.mockResolvedValue(emptyUsage)
    const { default: JevSection } = await import('./JevSection.jsx')
    const { unmount } = render(<JevSection part="machine" />)
    await screen.findByTestId('jev-key-chip')
    expect(screen.queryByText('Features')).not.toBeInTheDocument()
    expect(screen.queryByText(/Usage · last 7 days/)).not.toBeInTheDocument()
    unmount()
    render(<JevSection part="profile" />)
    await screen.findByText('Features')
    expect(screen.getByText(/Usage · last 7 days/)).toBeInTheDocument()
    expect(screen.getByText(/for this profile only/)).toBeInTheDocument()
    expect(screen.queryByTestId('jev-key-chip')).not.toBeInTheDocument()
  })

  it('reads the status and the usage once when it mounts alone', async () => {
    await mount(status('vault', 'typesafe'))
    expect(App.JevStatus).toHaveBeenCalledTimes(1)
    expect(App.JevUsage).toHaveBeenCalledTimes(1)
    expect(App.JevProviders).toHaveBeenCalledTimes(1)
  })

  // D4: with no provider configured the features read as before — no provider
  // select, today's TypeSafe wording, no provider column.
  it('keeps the TypeSafe wording and no provider select when only TypeSafe is configured', async () => {
    App.JevStatus.mockResolvedValue({ ...status('vault', 'typesafe'), provider: 'typesafe', provider_host: 'api.typesafe.ai', probability: 'native' })
    App.JevUsage.mockResolvedValue({ surfaces: [{ surface: 'hil', calls: 1, input_tokens: 10 }], total: { surface: 'total', calls: 1 } })
    const { default: JevSection } = await import('./JevSection.jsx')
    render(<JevSection />)
    await screen.findByTestId('jev-key-chip')
    expect(within(row('hil')).getByText('What is sent to TypeSafe')).toBeInTheDocument()
    expect(within(row('hil')).queryByRole('combobox')).not.toBeInTheDocument()
    fireEvent.click(within(row('hil')).getByRole('switch'))
    expect(screen.getByTestId('jev-confirm-hil')).toHaveTextContent('Turning this on sends the following to TypeSafe:')
    expect(within(screen.getByRole('table')).queryByText('Provider')).not.toBeInTheDocument()
  })

  it('does not read the usage for the machine half on its own', async () => {
    App.JevStatus.mockResolvedValue(status('vault', 'typesafe'))
    const { default: JevSection } = await import('./JevSection.jsx')
    render(<JevSection part="machine" />)
    await screen.findByTestId('jev-key-chip')
    expect(App.JevStatus).toHaveBeenCalledTimes(1)
    expect(App.JevUsage).not.toHaveBeenCalled()
  })

  it('shows the environment variable source without Remove', async () => {
    await mount(status('env'))
    expect(screen.getByTestId('jev-key-chip')).toHaveTextContent('Environment variable')
    expect(screen.queryByRole('button', { name: 'Remove' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Test' })).toBeEnabled()
  })

  it('saves the typed key, clears the input and refreshes status', async () => {
    await mount(status('none'))
    App.JevSetKey.mockResolvedValue({ key_source: 'vault', key_entry: 'typesafe', replaced: false })
    const input = screen.getByLabelText('TypeSafe API key')
    expect(input).toHaveAttribute('type', 'password')
    expect(input).toHaveValue('')
    fireEvent.change(input, { target: { value: 'ts_live_abc' } })
    App.JevStatus.mockResolvedValue(status('vault', 'typesafe'))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(App.JevSetKey).toHaveBeenCalledWith('ts_live_abc'))
    await screen.findByText('Saved to vault entry "typesafe".')
    expect(input).toHaveValue('')
    expect(App.JevStatus).toHaveBeenCalledTimes(2)
    await waitFor(() => expect(screen.getByTestId('jev-key-chip')).toHaveTextContent('Vault entry "typesafe"'))
  })

  it('enabling shows the egress list and only calls the CLI after confirm', async () => {
    await mount(status('vault', 'typesafe'))
    fireEvent.click(within(row('hil')).getByRole('switch'))
    const confirm = screen.getByTestId('jev-confirm-hil')
    expect(within(confirm).getByText("the node's auto_decide policy text")).toBeInTheDocument()
    expect(App.JevSetSurface).not.toHaveBeenCalled()
    fireEvent.change(within(confirm).getByLabelText('Threshold'), { target: { value: '0.85' } })
    App.JevSetSurface.mockResolvedValue({ surface: 'hil', enabled: true, threshold: 0.85 })
    App.JevStatus.mockResolvedValue(status('vault', 'typesafe', { hil: { enabled: true, threshold: 0.85 } }))
    fireEvent.click(within(confirm).getByRole('button', { name: 'Send this and enable' }))
    await waitFor(() => expect(App.JevSetSurface).toHaveBeenCalledWith('hil', true, 0.85))
    await waitFor(() => expect(screen.queryByTestId('jev-confirm-hil')).not.toBeInTheDocument())
    expect(within(row('hil')).getByRole('switch')).toHaveAttribute('aria-checked', 'true')
  })

  it('cancelling the enable confirm sends nothing', async () => {
    await mount(status('vault', 'typesafe'))
    fireEvent.click(within(row('inbox')).getByRole('switch'))
    fireEvent.click(within(screen.getByTestId('jev-confirm-inbox')).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByTestId('jev-confirm-inbox')).not.toBeInTheDocument()
    expect(App.JevSetSurface).not.toHaveBeenCalled()
  })

  it('disables an enabled surface directly, and applies a threshold change', async () => {
    await mount(status('vault', 'typesafe', { hil: { enabled: true }, inbox: { enabled: true } }))
    App.JevSetSurface.mockResolvedValue({})
    fireEvent.click(within(row('hil')).getByRole('switch'))
    await waitFor(() => expect(App.JevSetSurface).toHaveBeenCalledWith('hil', false, 0))
    await waitFor(() => expect(within(row('inbox')).getByLabelText('Threshold')).toBeEnabled())
    fireEvent.change(within(row('inbox')).getByLabelText('Threshold'), { target: { value: '0.8' } })
    fireEvent.click(within(row('inbox')).getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(App.JevSetSurface).toHaveBeenCalledWith('inbox', true, 0.8))
  })

  it('shows a successful key test with models', async () => {
    await mount(status('vault', 'typesafe'))
    App.JevTestKey.mockResolvedValue({ ok: true, key_source: 'vault', models: ['jev-latest', 'jev-mini'], error: '' })
    fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByTestId('jev-test-result')).toHaveTextContent('Key works — models: jev-latest, jev-mini')
  })

  it('shows a failed key test', async () => {
    await mount(status('vault', 'typesafe'))
    App.JevTestKey.mockResolvedValue({ ok: false, key_source: 'vault', models: [], error: 'jev: HTTP 401: {"detail":{"error_type":"authentication_error","message":"Cannot authenticate with the server."}}' })
    fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByTestId('jev-test-result')).toHaveTextContent('Key test failed: HTTP 401 — Cannot authenticate with the server.')
  })

  it('removing the key needs an inline confirm', async () => {
    await mount(status('vault', 'typesafe'))
    App.JevRemoveKey.mockResolvedValue({ removed: 'typesafe' })
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    expect(App.JevRemoveKey).not.toHaveBeenCalled()
    const box = screen.getByTestId('jev-remove-confirm')
    expect(box).toHaveTextContent('Delete vault entry "typesafe"?')
    App.JevStatus.mockResolvedValue(status('none'))
    fireEvent.click(within(box).getByRole('button', { name: 'Delete key' }))
    await waitFor(() => expect(App.JevRemoveKey).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.getByTestId('jev-key-chip')).toHaveTextContent('Not set'))
  })

  it('shows CLI errors inline and a retry when status fails', async () => {
    App.JevStatus.mockRejectedValue(new Error('monoagentcli not found'))
    App.JevUsage.mockResolvedValue(emptyUsage)
    const { default: JevSection } = await import('./JevSection.jsx')
    render(<JevSection />)
    expect(await screen.findByText(/Couldn't read Jev settings: monoagentcli not found/)).toBeInTheDocument()
    App.JevStatus.mockResolvedValue(status('none'))
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByTestId('jev-key-chip')
  })

  it('renders usage per surface with titles and a total', async () => {
    App.JevStatus.mockResolvedValue(status('vault', 'typesafe'))
    App.JevUsage.mockResolvedValue({ surfaces: [
      { surface: 'hil', calls: 12, failures: 1, input_tokens: 4800, estimated_usd: 0.0002 },
      { surface: 'inbox', calls: 3, failures: 0, input_tokens: 900, estimated_usd: 0.00003 },
    ], total: { surface: 'total', calls: 15, failures: 1, input_tokens: 5700, estimated_usd: 0.00023 } })
    const { default: JevSection } = await import('./JevSection.jsx')
    render(<JevSection />)
    const table = await screen.findByRole('table')
    expect(within(table).getByText('Human-in-Loop suggestions')).toBeInTheDocument()
    expect(within(table).getByText('4,800')).toBeInTheDocument()
    expect(within(table).getByText('Total')).toBeInTheDocument()
    expect(within(table).getByText('<$0.0001')).toBeInTheDocument()
  })

  it('renders folded by default when collapsible is true, and expands on click/key', async () => {
    App.JevStatus.mockResolvedValue(status('vault', 'typesafe', { hil: { enabled: true } }))
    App.JevUsage.mockResolvedValue({ surfaces: [], total: { surface: 'total', calls: 5, estimated_usd: 0.001 } })
    const { default: JevSection } = await import('./JevSection.jsx')
    render(<JevSection collapsible defaultExpanded={false} />)

    await screen.findByTestId('jev-fold-key-chip')
    const toggle = screen.getByTestId('jev-fold-toggle')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByTestId('jev-fold-key-chip')).toHaveTextContent('Vault entry "typesafe"')
    expect(screen.getByTestId('jev-fold-features-badge')).toHaveTextContent('1 feature active')
    expect(screen.getByText('5 calls (7d) · $0.0010')).toBeInTheDocument()
    // The main card body is hidden
    expect(screen.queryByLabelText('TypeSafe API key')).not.toBeInTheDocument()

    // Click to expand
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(await screen.findByLabelText('TypeSafe API key')).toBeInTheDocument()

    // Enter key to fold
    fireEvent.keyDown(toggle, { key: 'Enter' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByLabelText('TypeSafe API key')).not.toBeInTheDocument()
  })

  it('renders expanded when defaultExpanded is true', async () => {
    App.JevStatus.mockResolvedValue(status('none'))
    App.JevUsage.mockResolvedValue(emptyUsage)
    const { default: JevSection } = await import('./JevSection.jsx')
    render(<JevSection collapsible defaultExpanded={true} />)

    const toggle = await screen.findByTestId('jev-fold-toggle')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(await screen.findByLabelText('TypeSafe API key')).toBeInTheDocument()
  })

  // A jump to the Jev settings (the API section's, the dashboard's) passes a token that is new each time:
  // the section opens on each one, also after it was folded by hand, and not on a re-render with the same token.
  it('expands each time its expand token changes, even after it was folded by hand', async () => {
    App.JevStatus.mockResolvedValue(status('none'))
    App.JevUsage.mockResolvedValue(emptyUsage)
    const { default: JevSection } = await import('./JevSection.jsx')
    const first = { section: 'jev' }
    const { rerender } = render(<JevSection collapsible expandToken={null} />)
    const toggle = () => screen.getByTestId('jev-fold-toggle')
    await screen.findByTestId('jev-fold-key-chip')
    expect(toggle()).toHaveAttribute('aria-expanded', 'false')

    rerender(<JevSection collapsible expandToken={first} />)
    expect(toggle()).toHaveAttribute('aria-expanded', 'true')

    fireEvent.click(toggle()) // folded by hand
    expect(toggle()).toHaveAttribute('aria-expanded', 'false')
    rerender(<JevSection collapsible expandToken={first} />) // the same token: a re-render, not a jump
    expect(toggle()).toHaveAttribute('aria-expanded', 'false')

    rerender(<JevSection collapsible expandToken={{ section: 'jev' }} />) // a jump again
    expect(toggle()).toHaveAttribute('aria-expanded', 'true')

    fireEvent.click(toggle())
    rerender(<JevSection collapsible expandToken={null} />) // no token: nothing to open
    expect(toggle()).toHaveAttribute('aria-expanded', 'false')
  })

  it('opens on a token it is mounted with', async () => {
    App.JevStatus.mockResolvedValue(status('none'))
    App.JevUsage.mockResolvedValue(emptyUsage)
    const { default: JevSection } = await import('./JevSection.jsx')
    render(<JevSection collapsible expandToken={{ section: 'jev' }} />)
    expect(await screen.findByTestId('jev-fold-toggle')).toHaveAttribute('aria-expanded', 'true')
  })

  it('renders fold toggle while loading when collapsible is true', async () => {
    let resolveStatus
    App.JevStatus.mockReturnValue(new Promise(r => { resolveStatus = r }))
    App.JevUsage.mockResolvedValue(emptyUsage)
    const { default: JevSection } = await import('./JevSection.jsx')
    render(<JevSection collapsible defaultExpanded={false} />)

    const toggle = await screen.findByTestId('jev-fold-toggle')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByText(/Loading…/)).toBeInTheDocument()

    resolveStatus(status('none'))
    await waitFor(() => expect(screen.getByTestId('jev-fold-key-chip')).toHaveTextContent('Not set'))
  })
})


// @vitest-environment jsdom
// Settings shows two Jev parts (the machine key, the profile's features) off one status and one usage read, and
// refreshes both after a change made in either.
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, waitFor, fireEvent, within } from '@testing-library/react'
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: k => k, i18n: { resolvedLanguage: 'en', language: 'en', changeLanguage: vi.fn() } }),
}))
vi.mock('../services/api.js', () => ({
  api: { getDBPath: () => Promise.resolve('/db'), isDBConnected: () => Promise.resolve(true), listConnections: () => Promise.resolve([]) },
}))
const App = vi.hoisted(() => ({}))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: () => Promise.resolve({ version: '1' }), CheckForUpdate: () => Promise.resolve({}), AppSelfUpdate: vi.fn(),
  GetActiveProfile: () => Promise.resolve({ id: 'work', name: 'Work' }),
  JevStatus: (...a) => App.JevStatus(...a), JevUsage: (...a) => App.JevUsage(...a),
  JevSetKey: (...a) => App.JevSetKey(...a), JevTestKey: (...a) => App.JevTestKey(...a),
  JevRemoveKey: (...a) => App.JevRemoveKey(...a), JevSetSurface: (...a) => App.JevSetSurface(...a),
}))
const stub = vi.hoisted(() => (name) => ({ default: () => <div>{name}</div> }))
vi.mock('../components/settings/HealthSection.jsx', () => stub('health'))
vi.mock('../components/settings/BrowserBindingsSection.jsx', () => stub('bindings'))
vi.mock('../components/settings/ExtensionSection.jsx', () => stub('extension'))
vi.mock('../components/settings/ApiSection.jsx', () => stub('api'))
vi.mock('../components/settings/CoderModeSection.jsx', () => stub('coder'))
vi.mock('../components/settings/api/ApiKeysSection.jsx', () => stub('api-keys'))
import Settings from './Settings.jsx'

const status = (key_source, enabled = false) => ({
  profile_id: 'work', key_source, key_entry: 'typesafe', model: 'jev-latest',
  surfaces: [{ surface: 'hil', title: 'Human-in-Loop suggestions', egress: ['x'], enabled, threshold: 0.9, default_threshold: 0.9 }],
})
const usage = { surfaces: [], total: { surface: 'total', calls: 0 } }

beforeEach(() => {
  for (const k of ['JevStatus', 'JevUsage', 'JevSetKey', 'JevTestKey', 'JevRemoveKey', 'JevSetSurface']) App[k] = vi.fn()
  App.JevUsage.mockResolvedValue(usage)
})
afterEach(cleanup)

const mount = async (st) => {
  App.JevStatus.mockResolvedValue(st)
  render(<Settings onNavigate={vi.fn()} />)
  await screen.findByTestId('jev-fold-key-chip')
  await screen.findByText('Human-in-Loop suggestions')
}

describe('Settings Jev parts', () => {
  it('reads the status once and the usage once for both parts', async () => {
    await mount(status('vault'))
    expect(App.JevStatus).toHaveBeenCalledTimes(1)
    expect(App.JevUsage).toHaveBeenCalledTimes(1)
    expect(App.JevUsage).toHaveBeenCalledWith('7d')
  })

  it('splits what the one read shows: the key in the machine group, the features in the profile group', async () => {
    await mount(status('vault'))
    fireEvent.click(screen.getByTestId('jev-fold-toggle'))
    const machine = screen.getByTestId('jev-section')
    const profile = screen.getByTestId('jev-features-section')
    expect(within(machine).getByTestId('jev-key-chip')).toHaveTextContent('Vault entry "typesafe"')
    expect(within(machine).getAllByRole('button', { name: 'Remove' })).toHaveLength(1)
    expect(within(machine).queryByText('Features')).toBeNull()
    expect(within(profile).getByText('Features')).toBeInTheDocument()
    expect(within(profile).queryByRole('button', { name: 'Remove' })).toBeNull()
    expect(within(profile).queryByTestId('jev-key-chip')).toBeNull()
  })

  it('shows a key still in the profile vault with the adopt hint and no Remove', async () => {
    await mount(status('profile-vault'))
    fireEvent.click(screen.getByTestId('jev-fold-toggle'))
    expect(screen.getByTestId('jev-key-chip')).toHaveTextContent('settings.jev.keyProfileVault')
    expect(screen.getByTestId('jev-adopt-hint')).toHaveTextContent('monoagentcli jev key adopt')
    expect(screen.queryByRole('button', { name: 'Remove' })).toBeNull()
  })

  it('refreshes both parts once after the key is removed in the machine part', async () => {
    await mount(status('vault'))
    fireEvent.click(screen.getByTestId('jev-fold-toggle'))
    App.JevRemoveKey.mockResolvedValue({ removed: 'typesafe' })
    App.JevStatus.mockResolvedValue(status('none', true))
    fireEvent.click(screen.getByRole('button', { name: 'Remove' }))
    fireEvent.click(within(screen.getByTestId('jev-remove-confirm')).getByRole('button', { name: 'Delete key' }))
    await waitFor(() => expect(screen.getByTestId('jev-key-chip')).toHaveTextContent('Not set'))
    // the profile part saw the same new status
    expect(screen.getByText('Enabled features stay inactive until a key is set.')).toBeInTheDocument()
    expect(App.JevStatus).toHaveBeenCalledTimes(2)
    expect(App.JevUsage).toHaveBeenCalledTimes(2)
  })

  it('refreshes both parts once after a feature is switched in the profile part', async () => {
    await mount(status('vault'))
    fireEvent.click(screen.getByTestId('jev-fold-toggle'))
    App.JevSetSurface.mockResolvedValue({})
    App.JevStatus.mockResolvedValue(status('vault', true))
    fireEvent.click(within(screen.getByTestId('jev-features-section')).getByRole('switch'))
    fireEvent.click(within(screen.getByTestId('jev-confirm-hil')).getByRole('button', { name: 'Send this and enable' }))
    await waitFor(() => expect(screen.getByTestId('jev-fold-key-chip')).toBeInTheDocument())
    await waitFor(() => expect(App.JevSetSurface).toHaveBeenCalledWith('hil', true, 0.9))
    await waitFor(() => expect(App.JevStatus).toHaveBeenCalledTimes(2))
    expect(App.JevUsage).toHaveBeenCalledTimes(2)
  })
})

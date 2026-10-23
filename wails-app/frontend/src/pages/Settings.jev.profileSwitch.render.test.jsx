// @vitest-environment jsdom
// The Jev profile part (feature toggles, thresholds, 7-day usage) follows the profile: when the page comes back
// after the profile was switched elsewhere, the Jev status and usage are read again, once.
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: k => k, i18n: { resolvedLanguage: 'en', language: 'en', changeLanguage: vi.fn() } }),
}))
vi.mock('../services/api.js', () => ({
  api: { getDBPath: () => Promise.resolve('/db'), isDBConnected: () => Promise.resolve(true), listConnections: () => Promise.resolve([]) },
}))
const App = vi.hoisted(() => ({}))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: () => Promise.resolve({ version: '1' }), CheckForUpdate: () => Promise.resolve({}), AppSelfUpdate: vi.fn(),
  GetActiveProfile: (...a) => App.GetActiveProfile(...a),
  JevStatus: (...a) => App.JevStatus(...a), JevUsage: (...a) => App.JevUsage(...a),
  JevSetKey: vi.fn(), JevTestKey: vi.fn(), JevRemoveKey: vi.fn(), JevSetSurface: vi.fn(),
}))
const stub = vi.hoisted(() => (name) => ({ default: () => <div>{name}</div> }))
vi.mock('../components/settings/HealthSection.jsx', () => stub('health'))
vi.mock('../components/settings/BrowserBindingsSection.jsx', () => stub('bindings'))
vi.mock('../components/settings/ExtensionSection.jsx', () => stub('extension'))
vi.mock('../components/settings/ApiSection.jsx', () => stub('api'))
vi.mock('../components/settings/CoderModeSection.jsx', () => stub('coder'))
vi.mock('../components/settings/api/ApiKeysSection.jsx', () => stub('api-keys'))
import Settings from './Settings.jsx'

const status = (profile_id) => ({
  profile_id, key_source: 'vault', key_entry: 'typesafe', model: 'jev-latest',
  surfaces: [{ surface: 'hil', title: 'Human-in-Loop suggestions', egress: ['x'], enabled: false, threshold: 0.9, default_threshold: 0.9 }],
})

beforeEach(() => {
  App.GetActiveProfile = vi.fn().mockResolvedValue({ id: 'work', name: 'Work' })
  App.JevStatus = vi.fn().mockResolvedValue(status('work'))
  App.JevUsage = vi.fn().mockResolvedValue({ surfaces: [], total: { surface: 'total', calls: 0 } })
})
afterEach(cleanup)

describe('Settings Jev follows the profile', () => {
  it('re-reads the Jev status and usage once when the page returns after a profile switch', async () => {
    const view = render(<Settings onNavigate={vi.fn()} isActive />)
    await screen.findByTestId('jev-fold-key-chip')
    await waitFor(() => expect(App.GetActiveProfile).toHaveBeenCalledTimes(1))
    expect(App.JevStatus).toHaveBeenCalledTimes(1)

    view.rerender(<Settings onNavigate={vi.fn()} isActive={false} />)
    App.GetActiveProfile.mockResolvedValue({ id: 'home', name: 'Home' })
    App.JevStatus.mockResolvedValue(status('home'))
    view.rerender(<Settings onNavigate={vi.fn()} isActive />)

    await waitFor(() => expect(App.JevStatus).toHaveBeenCalledTimes(2))
    expect(App.JevUsage).toHaveBeenCalledTimes(2)
    // Let any further (wrong) re-read land before counting.
    await new Promise(r => setTimeout(r, 50))
    expect(App.JevStatus).toHaveBeenCalledTimes(2)
    expect(App.JevUsage).toHaveBeenCalledTimes(2)
  })
})

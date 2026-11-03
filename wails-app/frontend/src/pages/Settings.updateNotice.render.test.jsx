// @vitest-environment jsdom
// The update line of Settings: an install that comes with a notice shows it and waits for the person.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k, o) => (o?.name ? `${k}:${o.name}` : k),
    i18n: { resolvedLanguage: 'en', language: 'en', changeLanguage: vi.fn() },
  }),
}))
vi.mock('../services/api.js', () => ({
  api: { getDBPath: () => Promise.resolve('/db'), isDBConnected: () => Promise.resolve(true), listConnections: () => Promise.resolve([]) },
}))
const quit = vi.hoisted(() => vi.fn())
const result = vi.hoisted(() => ({ value: null }))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: () => Promise.resolve({ version: '1' }),
  CheckForUpdate: () => Promise.resolve({ update_available: true, latest_version: 'v9.9.9' }),
  AppSelfUpdate: () => Promise.resolve(result.value), QuitApp: quit,
  GetActiveProfile: vi.fn(() => Promise.resolve({ id: 'work', name: 'Work' })),
}))
const stub = vi.hoisted(() => (name) => ({ default: ({ part }) => <div data-testid={part ? `${name}-${part}` : name}>{name}</div> }))
vi.mock('../components/settings/HealthSection.jsx', () => stub('health'))
vi.mock('../components/settings/BrowserBindingsSection.jsx', () => stub('bindings'))
vi.mock('../components/settings/ExtensionSection.jsx', () => stub('extension'))
vi.mock('../components/settings/JevSection.jsx', () => stub('jev'))
vi.mock('../components/settings/ApiSection.jsx', () => stub('api'))
vi.mock('../components/settings/CoderModeSection.jsx', () => stub('coder'))
vi.mock('../components/settings/CoderOrgSection.jsx', () => stub('coder-org'))
vi.mock('../components/settings/BrowserPermissionsSection.jsx', () => stub('browser-permissions'))
vi.mock('../components/settings/api/ApiKeysSection.jsx', () => stub('api-keys'))
import Settings from './Settings.jsx'

afterEach(() => { cleanup(); vi.clearAllMocks() })

async function install() {
  render(<Settings onNavigate={vi.fn()} />)
  fireEvent.click(await screen.findByText('Check for updates'))
  fireEvent.click(await screen.findByText('Install Update'))
}

describe('Settings update notice', () => {
  it('shows the notice, and "Restart now" quits', async () => {
    result.value = { success: true, new_version: 'v9.9.9', notice: 'daemon is down\nThe update is installed; restart the app to use it.' }
    await install()
    expect(await screen.findByTestId('update-notice')).toHaveTextContent('daemon is down')
    expect(quit).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('update-notice-restart'))
    expect(quit).toHaveBeenCalledTimes(1)
  })

  it('"Later" hides the notice and keeps the restart line', async () => {
    result.value = { success: true, new_version: 'v9.9.9', notice: 'n' }
    await install()
    fireEvent.click(await screen.findByTestId('update-notice-later'))
    expect(screen.queryByTestId('update-notice')).toBeNull()
    expect(screen.getByText(/Updated to v9.9.9 — restart the app to apply/)).toBeInTheDocument()
  })

  it('has no notice box for a clean install', async () => {
    result.value = { success: true, new_version: 'v9.9.9' }
    await install()
    await screen.findByText(/Updated to v9.9.9/)
    expect(screen.queryByTestId('update-notice')).toBeNull()
  })
})

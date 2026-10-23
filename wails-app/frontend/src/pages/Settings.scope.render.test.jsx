// @vitest-environment jsdom
// Settings groups its sections by scope: machine-wide settings first, under their own heading, then the
// active profile's, named. Each section sits in the group its data belongs to.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, within, waitFor } from '@testing-library/react'
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k, o) => (o?.name ? `${k}:${o.name}` : k),
    i18n: { resolvedLanguage: 'en', language: 'en', changeLanguage: vi.fn() },
  }),
}))
vi.mock('../services/api.js', () => ({
  api: { getDBPath: () => Promise.resolve('/db'), isDBConnected: () => Promise.resolve(true), listConnections: () => Promise.resolve([]) },
}))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: () => Promise.resolve({ version: '1' }), CheckForUpdate: () => Promise.resolve({}), AppSelfUpdate: vi.fn(),
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
import { GetActiveProfile } from '../wailsjs/go/main/App'

afterEach(cleanup)

describe('Settings scope groups', () => {
  it('puts the machine-wide group first and the profile group below it', async () => {
    render(<Settings onNavigate={vi.fn()} />)
    const g = screen.getByTestId('scope-global')
    const p = screen.getByTestId('scope-profile')
    expect(g.compareDocumentPosition(p) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(within(g).getByText('settings.scope.globalTitle')).toBeInTheDocument()
    expect(screen.getByTestId('scope-global-line')).toHaveTextContent('settings.scope.globalLine')
    await waitFor(() => expect(screen.getByTestId('scope-profile-line')).toHaveTextContent('settings.scope.profileLine:Work'))
  })

  it('falls back to a nameless line while the profile name is unknown', () => {
    render(<Settings onNavigate={vi.fn()} />)
    expect(screen.getByTestId('scope-profile-line')).toHaveTextContent('settings.scope.profileLineNoName')
  })

  it('files every section under the group its data belongs to', () => {
    render(<Settings onNavigate={vi.fn()} />)
    const g = screen.getByTestId('scope-global')
    const p = screen.getByTestId('scope-profile')
    for (const id of ['health', 'coder', 'coder-org-machine', 'browser-permissions', 'extension', 'jev-machine', 'api-machine']) expect(within(g).getByTestId(id)).toBeInTheDocument()
    expect(within(g).getByText('Database Path')).toBeInTheDocument()
    expect(within(g).getByText('settings.aiAgentsTitle')).toBeInTheDocument()
    expect(within(p).getByTestId('bindings')).toBeInTheDocument()
    expect(within(p).getAllByText('Data Export').length).toBeGreaterThan(0)
    expect(within(p).getByText('settings.connectionsTitle')).toBeInTheDocument()
    expect(within(g).queryByText('Data Export')).toBeNull()
    // the sections that mix both scopes are split: the profile's halves are in the profile group only
    expect(within(p).getByTestId('jev-profile')).toBeInTheDocument()
    expect(within(p).getByTestId('api-keys')).toBeInTheDocument()
    // Coder org settings are this computer's; its always-allow list is the profile's.
    expect(within(p).getByTestId('coder-org-profile')).toBeInTheDocument()
    expect(within(g).queryByTestId('coder-org-profile')).toBeNull()
    expect(within(p).queryByTestId('coder-org-machine')).toBeNull()
    expect(within(g).queryByTestId('jev-profile')).toBeNull()
    expect(within(g).queryByTestId('api-keys')).toBeNull()
    expect(within(p).queryByTestId('jev-machine')).toBeNull()
    expect(within(p).queryByTestId('api-machine')).toBeNull()
  })

  it('reads the profile again when the page becomes active, as the app keeps it mounted', async () => {
    const { rerender } = render(<Settings onNavigate={vi.fn()} isActive />)
    await waitFor(() => expect(screen.getByTestId('scope-profile-line')).toHaveTextContent('profileLine:Work'))
    rerender(<Settings onNavigate={vi.fn()} isActive={false} />)
    GetActiveProfile.mockResolvedValueOnce({ id: 'home', name: 'Home' })
    rerender(<Settings onNavigate={vi.fn()} isActive />)
    await waitFor(() => expect(screen.getByTestId('scope-profile-line')).toHaveTextContent('profileLine:Home'))
  })
})

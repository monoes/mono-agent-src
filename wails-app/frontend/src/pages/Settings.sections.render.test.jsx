// @vitest-environment jsdom
// W4 r30-3: the sections the Help page links to (extension, api, coder) are deep-link targets.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, cleanup } from '@testing-library/react'
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: k => k, i18n: { resolvedLanguage: 'en', language: 'en', changeLanguage: vi.fn() } }),
}))
vi.mock('../services/api.js', () => ({
  api: { getDBPath: () => Promise.resolve('/db'), isDBConnected: () => Promise.resolve(true), listConnections: () => Promise.resolve([]) },
}))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: () => Promise.resolve({ version: '1' }), CheckForUpdate: () => Promise.resolve({}), AppSelfUpdate: vi.fn(),
  GetActiveProfile: vi.fn(() => Promise.resolve({ id: 'work', name: 'Work' })),
}))
const stub = vi.hoisted(() => (name) => ({ default: () => <div>{name}</div> }))
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

afterEach(cleanup)

describe('Settings deep links', () => {
  it.each(['extension', 'api', 'coder'])('scrolls to the %s section', (section) => {
    const calls = []
    Element.prototype.scrollIntoView = function () { calls.push(this.getAttribute('data-section')) }
    render(<Settings onNavigate={vi.fn()} navData={{ section }} />)
    expect(calls).toEqual([section])
  })
})

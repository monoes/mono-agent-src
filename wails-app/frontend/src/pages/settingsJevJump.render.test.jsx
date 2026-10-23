// @vitest-environment jsdom
// The jump from the API section to the Jev settings, with the real Jev section (the deep-link test next door
// mocks it, and so only sees the scrolling): Settings scrolls to it and opens it, on every jump, also when the
// user folded it by hand after the last one.
import { useState } from 'react'
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: k => k, i18n: { resolvedLanguage: 'en', language: 'en', changeLanguage: vi.fn() } }) }))
vi.mock('../services/api.js', () => ({
  api: { getDBPath: () => Promise.resolve('/db'), isDBConnected: () => Promise.resolve(true), listConnections: () => Promise.resolve([]) },
}))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: () => Promise.resolve({ version: '1' }), CheckForUpdate: () => Promise.resolve({}), AppSelfUpdate: vi.fn(),
  JevStatus: () => Promise.resolve({ profile_id: 'default', key_source: 'none', surfaces: [] }),
  JevUsage: () => Promise.resolve({ surfaces: [], total: { surface: 'total', calls: 0 } }),
  JevSetKey: vi.fn(), JevTestKey: vi.fn(), JevRemoveKey: vi.fn(), JevSetSurface: vi.fn(),
}))
vi.mock('../components/settings/ExtensionSection.jsx', () => ({ default: () => <div>extension-section</div> }))
vi.mock('../components/settings/HealthSection.jsx', () => ({ default: () => <div>health-section</div> }))
// The API section asks Settings to take it to the Jev settings, as its "Open the Jev settings" button does.
vi.mock('../components/settings/ApiSection.jsx', () => ({
  default: ({ onNavigate }) => <button onClick={() => onNavigate('settings', { section: 'jev' })}>api-section</button>,
}))
import Settings from './Settings.jsx'

let scrolled
beforeEach(() => {
  scrolled = []
  Element.prototype.scrollIntoView = function () { scrolled.push(this.dataset.section) }
})
afterEach(cleanup)

const jev = () => screen.getByTestId('jev-fold-toggle')

// What App.jsx does with navigate(page, data): the page gets the data as navData, a new object each time.
function App() {
  const [navData, setNavData] = useState(null)
  return <Settings onNavigate={(page, data) => setNavData(data || null)} navData={navData} />
}

describe('the jump to the Jev settings', () => {
  it('opens the Jev section and scrolls to it, on every jump, also after it was folded by hand', async () => {
    render(<App />)
    await screen.findByTestId('jev-fold-key-chip')
    expect(jev()).toHaveAttribute('aria-expanded', 'false')

    fireEvent.click(screen.getByText('api-section'))
    await waitFor(() => expect(jev()).toHaveAttribute('aria-expanded', 'true'))
    expect(scrolled).toEqual(['jev'])

    fireEvent.click(jev()) // folded by hand
    expect(jev()).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(screen.getByText('api-section')) // the same jump again: Settings has not left, navData has the same shape
    await waitFor(() => expect(jev()).toHaveAttribute('aria-expanded', 'true'))
    expect(scrolled).toEqual(['jev', 'jev'])
  })

  it('opens when Settings is opened by the jump, as the dashboard does', async () => {
    render(<Settings onNavigate={vi.fn()} navData={{ section: 'jev' }} />)
    expect(await screen.findByTestId('jev-fold-toggle')).toHaveAttribute('aria-expanded', 'true')
    await waitFor(() => expect(scrolled).toEqual(['jev']))
  })

  it('does not reopen a section the user folded when Settings renders again with the same navigation', async () => {
    const nav = { section: 'jev' }
    const { rerender } = render(<Settings onNavigate={vi.fn()} navData={nav} />)
    await waitFor(() => expect(jev()).toHaveAttribute('aria-expanded', 'true'))
    fireEvent.click(jev())
    rerender(<Settings onNavigate={vi.fn()} navData={nav} />)
    expect(jev()).toHaveAttribute('aria-expanded', 'false')
  })

  it('leaves the Jev section alone for a jump to another section, or none', async () => {
    const { rerender } = render(<Settings onNavigate={vi.fn()} navData={{ section: 'health' }} />)
    await screen.findByTestId('jev-fold-key-chip')
    expect(jev()).toHaveAttribute('aria-expanded', 'false')
    rerender(<Settings onNavigate={vi.fn()} navData={null} />)
    expect(jev()).toHaveAttribute('aria-expanded', 'false')
    await waitFor(() => expect(scrolled).toEqual(['health']))
  })
})

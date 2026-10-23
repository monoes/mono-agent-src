// @vitest-environment jsdom
// Dashboard deep links: Settings scrolls to a section, Connections opens an
// automation's drawer on the tab the link names.
import { useState } from 'react'
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, waitFor, fireEvent, act } from '@testing-library/react'
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: k => k, i18n: { resolvedLanguage: 'en', language: 'en', changeLanguage: vi.fn() } }) }))
vi.mock('../services/api.js', () => ({
  api: {
    getDBPath: () => Promise.resolve('/db'), isDBConnected: () => Promise.resolve(true),
    listConnections: () => Promise.resolve([]), listPlatforms: () => Promise.resolve([]),
    listAutomations: () => Promise.resolve({ automations: [{ id: 'linkedin', name: 'LinkedIn' }, { id: 'x', name: 'X' }] }),
  },
}))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: () => Promise.resolve({ version: '1' }), CheckForUpdate: () => Promise.resolve({}), AppSelfUpdate: vi.fn(),
}))
vi.mock('../components/settings/ExtensionSection.jsx', () => ({ default: () => <div>extension-section</div> }))
vi.mock('../components/settings/HealthSection.jsx', () => ({ default: () => <div>health-section</div> }))
vi.mock('../components/settings/JevSection.jsx', () => ({ default: () => <div>jev-section</div> }))
// The API section asks Settings to take it to the Jev settings, as the dashboard's Jev row does, and is told
// whether Settings is the page on show (the app keeps it mounted, hidden, when another page is).
vi.mock('../components/settings/ApiSection.jsx', () => ({
  default: ({ onNavigate, isActive }) => <button data-active={String(isActive)} onClick={() => onNavigate('settings', { section: 'jev' })}>api-section</button>,
}))
vi.mock('./connections/BrowserAutomations.jsx', () => ({
  default: ({ onOpen }) => <div>automations<button onClick={() => onOpen({ id: 'x' })}>open-x</button></div>,
  RecordHelpDialog: () => null,
}))
vi.mock('./connections/ApiConnections.jsx', () => ({ default: () => null, resolveConn: () => null }))
vi.mock('./connections/ApiConnectionModal.jsx', () => ({ default: () => null }))
vi.mock('./connections/ImportDialog.jsx', () => ({ default: () => null }))
vi.mock('./connections/AutomationDrawer.jsx', () => ({
  default: ({ automation, initialTab, onClose, onChanged }) => (
    <div>
      <span>drawer:{automation.id}:{initialTab}</span>
      <button onClick={onClose}>close</button><button onClick={onChanged}>changed</button>
    </div>
  ),
}))
import Settings from './Settings.jsx'
import Connections from './Connections.jsx'

let scrolled
beforeEach(() => {
  scrolled = []
  Element.prototype.scrollIntoView = function () { scrolled.push(this.dataset.section) }
})
afterEach(cleanup)

describe('dashboard deep links', () => {
  it('Settings scrolls to the section navData names', async () => {
    render(<Settings onNavigate={vi.fn()} navData={{ section: 'jev' }} />)
    await waitFor(() => expect(scrolled).toEqual(['jev']))
  })
  it('Settings without navData does not scroll', () => {
    render(<Settings onNavigate={vi.fn()} navData={null} />)
    expect(scrolled).toEqual([])
  })
  it("the API section's jump takes Settings to the Jev section, through the app's own navigate", async () => {
    // What App.jsx does with navigate(page, data): the page gets the data as navData.
    function App() {
      const [navData, setNavData] = useState(null)
      return <Settings onNavigate={(page, data) => setNavData(data || null)} navData={navData} />
    }
    render(<App />)
    expect(scrolled).toEqual([])
    fireEvent.click(screen.getByText('api-section'))
    await waitFor(() => expect(scrolled).toEqual(['jev']))
  })
  it('Settings tells the API section whether it is the page on show, so that it reads again on coming back', () => {
    const { rerender } = render(<Settings onNavigate={vi.fn()} navData={null} />)
    expect(screen.getByText('api-section')).toHaveAttribute('data-active', 'true') // without being told, it always is
    rerender(<Settings onNavigate={vi.fn()} navData={null} isActive={false} />)
    expect(screen.getByText('api-section')).toHaveAttribute('data-active', 'false')
    rerender(<Settings onNavigate={vi.fn()} navData={null} isActive />)
    expect(screen.getByText('api-section')).toHaveAttribute('data-active', 'true')
  })
  it('Connections opens the named automation on the named tab', async () => {
    render(<Connections navData={{ automationId: 'linkedin', tab: 'health' }} />)
    expect(await screen.findByText('drawer:linkedin:Health')).toBeInTheDocument()
  })
  it('a deep link is applied once: reloading the list later does not reopen it', async () => {
    render(<Connections navData={{ automationId: 'linkedin', tab: 'health' }} />)
    await screen.findByText('drawer:linkedin:Health')
    fireEvent.click(screen.getByText('close'))
    fireEvent.click(screen.getByText('open-x'))
    await screen.findByText('drawer:x:Overview')
    await act(async () => { fireEvent.click(screen.getByText('changed')) })
    await new Promise(r => setTimeout(r, 20))
    expect(screen.getByText('drawer:x:Overview')).toBeInTheDocument()
  })
  it('Connections ignores an unknown automation', async () => {
    render(<Connections navData={{ automationId: 'nope', tab: 'health' }} />)
    await screen.findByText('automations')
    expect(screen.queryByText(/drawer:/)).toBeNull()
  })
})

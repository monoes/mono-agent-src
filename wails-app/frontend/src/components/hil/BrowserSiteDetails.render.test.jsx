// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, within, cleanup } from '@testing-library/react'
import HumanInLoop from '../../pages/HumanInLoop.jsx'
import * as WailsApp from '../../wailsjs/go/main/App'
import { api } from '../../services/api.js'
// A `browser.site` item as `monoagentcli --json hil list` lists it (plan C8:
// readonly_data {origin, url, kind, requested_by}), written against the
// contract before the CLI that stores it landed.
import fixture from './__fixtures__/browser-site-item.json'
// The same for a look-alike host (https://аpple.com, a Cyrillic а):
// origin in xn-- form, asciiHost and unicodeHost.
import idnFixture from './__fixtures__/browser-site-item-idn.json'

vi.mock('../../wailsjs/go/main/App', () => ({
  GetHILItems: vi.fn(),
  ApproveHIL: vi.fn(),
  RejectHIL: vi.fn(),
  GetDraftPersonMessages: vi.fn(),
  SendDraftPersonMessage: vi.fn(),
  RejectDraftPersonMessage: vi.fn(),
}))
vi.mock('../../services/api.js', () => ({
  api: { getPendingPeopleApprovals: vi.fn(), listOrgPending: vi.fn(), readHILScreenshot: vi.fn(), openURL: vi.fn() },
  notify: vi.fn(),
}))
vi.mock('../../lib/usePageVisible.js', () => ({
  usePageVisibleRef: () => ({ current: true }),
  useVisibleCatchUp: () => {},
}))

beforeEach(() => {
  WailsApp.GetHILItems.mockResolvedValue([structuredClone(fixture)])
  WailsApp.GetDraftPersonMessages.mockResolvedValue([])
  WailsApp.ApproveHIL.mockResolvedValue()
  WailsApp.RejectHIL.mockResolvedValue()
  api.getPendingPeopleApprovals.mockResolvedValue([])
  api.listOrgPending.mockResolvedValue({ items: [] })
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('browser site approval card', () => {
  it('says which agent wants which site, with the page and who asked', async () => {
    render(<HumanInLoop />)
    const card = await screen.findByTestId('browser-site-details')
    expect(card).toHaveTextContent('A coder chat wants to use https://x.com')
    expect(within(card).getByText('https://x.com/monoes/status/1846')).toBeInTheDocument()
    expect(within(card).getByText('an agent (claims: coder chat agent · chat "040abd27")')).toBeInTheDocument()
    expect(screen.queryByText('Context Info')).toBeNull()
    expect(screen.getByText('browser')).toBeInTheDocument()
  })

  it('shows the ASCII form of a non-Latin host with a check-the-address hint, and nothing for an ASCII host', async () => {
    WailsApp.GetHILItems.mockResolvedValue([structuredClone(idnFixture)])
    const { unmount } = render(<HumanInLoop />)
    const card = await screen.findByTestId('browser-site-details')
    expect(card).toHaveTextContent('A coder chat wants to use https://аpple.com')
    const check = within(card).getByTestId('ascii-host')
    expect(check).toHaveTextContent('ASCII form: xn--pple-43d.com')
    expect(check).toHaveTextContent('Check the address: this site uses non-Latin characters')
    unmount()

    WailsApp.GetHILItems.mockResolvedValue([structuredClone(fixture)])
    render(<HumanInLoop />)
    expect(await screen.findByTestId('browser-site-details')).toBeInTheDocument()
    expect(screen.queryByTestId('ascii-host')).toBeNull()
  })

  it('Allow approves the item and Not now rejects it', async () => {
    render(<HumanInLoop />)
    await screen.findByTestId('browser-site-details')
    expect(screen.queryByRole('button', { name: /Approve & Continue/ })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Allow' }))
    await waitFor(() => expect(WailsApp.ApproveHIL).toHaveBeenCalledWith(fixture.id, '{}'))
    fireEvent.click(screen.getByRole('button', { name: 'Not now' }))
    await waitFor(() => expect(WailsApp.RejectHIL).toHaveBeenCalledWith(fixture.id))
  })

  it('names the other agent kinds', async () => {
    const org = structuredClone(fixture)
    org.readonly_data.kind = 'org'
    org.readonly_data.requested_by = { role: 'qa', org: 'growth' }
    WailsApp.GetHILItems.mockResolvedValue([org])
    render(<HumanInLoop />)
    const card = await screen.findByTestId('browser-site-details')
    expect(card).toHaveTextContent('An org role wants to use https://x.com')
    expect(within(card).getByText('an agent (claims: role "qa" · org "growth")')).toBeInTheDocument()
  })

  it("shows MonoAgent's own label as it is and an agent's description of itself as its claim", async () => {
    const engine = structuredClone(fixture)
    engine.readonly_data.kind = 'other'
    engine.readonly_data.requested_by = { agentId: 'unapproved-workflow', system: true }
    WailsApp.GetHILItems.mockResolvedValue([engine])
    const { unmount } = render(<HumanInLoop />)
    expect(within(await screen.findByTestId('browser-site-details')).getByText('agent unapproved-workflow')).toBeInTheDocument()
    unmount()

    const forged = structuredClone(fixture)
    forged.readonly_data.requested_by = { role: 'You (approved earlier today)', org: 'growth' }
    WailsApp.GetHILItems.mockResolvedValue([forged])
    render(<HumanInLoop />)
    const card = await screen.findByTestId('browser-site-details')
    expect(within(card).getByText('an agent (claims: role "You (approved earlier today)" · org "growth")')).toBeInTheDocument()
  })
})

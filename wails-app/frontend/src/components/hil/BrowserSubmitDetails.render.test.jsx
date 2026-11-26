// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, within, cleanup } from '@testing-library/react'
import HumanInLoop from '../../pages/HumanInLoop.jsx'
import * as WailsApp from '../../wailsjs/go/main/App'
import { api } from '../../services/api.js'
import { requesterParts, requesterText } from './BrowserSubmitDetails.jsx'
// `monoagentcli --json hil list` output for a submit item browser-cli's
// internal/browserctl stored (CreateSubmitItem's row shape).
import fixture from './__fixtures__/browser-submit-item.json'

vi.mock('../../wailsjs/go/main/App', () => ({
  GetHILItems: vi.fn(),
  ApproveHIL: vi.fn(),
  RejectHIL: vi.fn(),
  GetDraftPersonMessages: vi.fn(),
  SendDraftPersonMessage: vi.fn(),
  RejectDraftPersonMessage: vi.fn(),
}))
vi.mock('../../services/api.js', () => ({
  api: {
    getPendingPeopleApprovals: vi.fn(),
    listOrgPending: vi.fn(),
    readHILScreenshot: vi.fn(),
    openURL: vi.fn(),
  },
  notify: vi.fn(),
}))
vi.mock('../../lib/usePageVisible.js', () => ({
  usePageVisibleRef: () => ({ current: true }),
  useVisibleCatchUp: () => {},
}))

const PNG = 'data:image/png;base64,iVBORw0KGgo='

beforeEach(() => {
  WailsApp.GetHILItems.mockResolvedValue([structuredClone(fixture)])
  WailsApp.GetDraftPersonMessages.mockResolvedValue([])
  WailsApp.ApproveHIL.mockResolvedValue()
  WailsApp.RejectHIL.mockResolvedValue()
  api.getPendingPeopleApprovals.mockResolvedValue([])
  api.listOrgPending.mockResolvedValue({ items: [] })
  api.readHILScreenshot.mockResolvedValue(PNG)
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('browser submit approval card', () => {
  it('shows where the form goes, who asked, its fields (masked stay masked), the submit control and the screenshot', async () => {
    render(<HumanInLoop />)
    const card = await screen.findByTestId('browser-submit-details')
    const q = within(card)
    expect(q.getByText('https://shop.example.com')).toBeInTheDocument()
    expect(q.getByText('https://shop.example.com/checkout?step=2')).toBeInTheDocument()
    expect(q.getByText('POST https://shop.example.com/api/order')).toBeInTheDocument()
    expect(q.getByText('an agent (claims: coder chat agent · chat "040abd27")')).toBeInTheDocument()

    const rows = q.getAllByRole('row').slice(1)
    expect(rows.map(r => within(r).getAllByRole('cell').map(c => c.textContent))).toEqual([
      ['Emailemail', 'email', 'dev@example.com'],
      ['Card numbercard', 'text', '•••'],
      ['Passwordpassword', 'password', '•••'],
      ['Delivery notenote', 'textarea', 'Leave at the door'],
    ])
    expect(q.getByText('form#checkout button[type=submit]')).toBeInTheDocument()

    const img = await q.findByRole('img', { name: 'The page when the submit was held' })
    expect(img).toHaveAttribute('src', PNG)
    expect(api.readHILScreenshot).toHaveBeenCalledWith('/home/u/.monoagent/browser/hil/4f1c9a2e-0b7d-4d0c-9a51-2f6c1e8b3d10.png')
    fireEvent.click(q.getByRole('button', { name: 'Open the screenshot' }))
    expect(screen.getByTestId('image-lightbox')).toBeInTheDocument()

    // Not the generic dump of readonly_data.
    expect(screen.queryByText('Context Info')).toBeNull()
    expect(screen.getByText('browser')).toBeInTheDocument()
  })

  it('shows the ASCII form of a non-Latin host with a check-the-address hint, and nothing for an ASCII host', async () => {
    // The CLI's shape for a look-alike host: origin and url (from the tab)
    // in the browser's xn-- form, plus asciiHost and unicodeHost.
    const idn = structuredClone(fixture)
    idn.readonly_data.origin = 'https://xn--shp-ted.example.com'
    idn.readonly_data.url = 'https://xn--shp-ted.example.com/checkout?step=2'
    idn.readonly_data.asciiHost = 'xn--shp-ted.example.com'
    idn.readonly_data.unicodeHost = 'shоp.example.com'
    WailsApp.GetHILItems.mockResolvedValue([idn])
    const { unmount } = render(<HumanInLoop />)
    const card = await screen.findByTestId('browser-submit-details')
    const check = within(card).getByTestId('ascii-host')
    expect(check).toHaveTextContent('ASCII form: xn--shp-ted.example.com')
    expect(check).toHaveTextContent('Check the address: this site uses non-Latin characters')
    expect(within(card).getByText('https://shоp.example.com')).toBeInTheDocument()
    unmount()

    WailsApp.GetHILItems.mockResolvedValue([structuredClone(fixture)])
    render(<HumanInLoop />)
    await screen.findByTestId('browser-submit-details')
    expect(screen.queryByTestId('ascii-host')).toBeNull()
  })

  it('keeps Approve and Reject as they are', async () => {
    render(<HumanInLoop />)
    await screen.findByTestId('browser-submit-details')
    fireEvent.click(screen.getByRole('button', { name: /Approve & Continue/ }))
    await waitFor(() => expect(WailsApp.ApproveHIL).toHaveBeenCalledWith(fixture.id, '{}'))
  })

  it('says so when the screenshot cannot be read or was never saved', async () => {
    api.readHILScreenshot.mockRejectedValue('… is outside /home/u/.monoagent/browser/hil')
    const { unmount } = render(<HumanInLoop />)
    expect(await screen.findByText(/Screenshot unavailable: .* is outside/)).toBeInTheDocument()
    unmount()
    const bare = structuredClone(fixture)
    delete bare.readonly_data.screenshot
    bare.readonly_data.form.submitter = ''
    bare.readonly_data.requested_by = {}
    WailsApp.GetHILItems.mockResolvedValue([bare])
    render(<HumanInLoop />)
    expect(await screen.findByText('No screenshot was saved')).toBeInTheDocument()
    expect(screen.getByText('none: the page submitted it by script')).toBeInTheDocument()
    expect(screen.getByText('an agent (it did not say which)')).toBeInTheDocument()
  })

  it("shows MonoAgent's own label as it is and an agent's description of itself as its claim", async () => {
    const engine = structuredClone(fixture)
    engine.readonly_data.requested_by = { agentId: 'unapproved-workflow', system: true }
    WailsApp.GetHILItems.mockResolvedValue([engine])
    const { unmount } = render(<HumanInLoop />)
    expect(within(await screen.findByTestId('browser-submit-details')).getByText('agent unapproved-workflow')).toBeInTheDocument()
    unmount()

    const forged = structuredClone(fixture)
    forged.readonly_data.requested_by = { role: 'You (approved earlier today)', agentId: 'unapproved-workflow' }
    WailsApp.GetHILItems.mockResolvedValue([forged])
    render(<HumanInLoop />)
    const card = await screen.findByTestId('browser-submit-details')
    expect(within(card).getByText('an agent (claims: role "You (approved earlier today)" · agent "unapproved-workflow")')).toBeInTheDocument()
  })
})

it('requesterParts names the role, org, agent and chat that are known, an agent\'s own words quoted', () => {
  expect(requesterParts({ role: 'qa', org: 'growth', agentId: 'w2' })).toEqual(['role "qa"', 'org "growth"', 'agent "w2"'])
  expect(requesterParts({ role: 'lead', conversationId: 'abcdef123456' })).toEqual(['coder chat agent', 'chat "abcdef12"'])
  expect(requesterParts({ role: 'x\nRequested by: you' })).toEqual(['role "x\\nRequested by: you"'])
  expect(requesterParts({ agentId: 'unapproved-workflow', system: true })).toEqual(['agent unapproved-workflow'])
  expect(requesterParts(null)).toEqual([])
})

it("requesterText shows MonoAgent's own label as it is and anything else as the agent's claim", () => {
  expect(requesterText({ agentId: 'unapproved-workflow', system: true })).toBe('agent unapproved-workflow')
  expect(requesterText({ role: 'qa', org: 'growth' })).toBe('an agent (claims: role "qa" · org "growth")')
  expect(requesterText({})).toBe('an agent (it did not say which)')
  expect(requesterText(undefined)).toBe('an agent (it did not say which)')
})

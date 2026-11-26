// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../../i18n.js'
import { HilScope } from './AllProfiles.jsx'
import * as WailsApp from '../../wailsjs/go/main/App'

vi.mock('../../wailsjs/go/main/App', () => ({
  HILPendingAll: vi.fn(),
  HILOrgPendingAll: vi.fn(),
}))

// One ref for the life of the page, as the real hook gives.
vi.mock('../../lib/usePageVisible.js', () => {
  const visible = { current: true }
  return { usePageVisibleRef: () => visible, useVisibleCatchUp: () => {} }
})

// A command with a right-to-left override in it, as long as monomind keeps.
const RLO = String.fromCharCode(0x202e)
const orgItems = [
  {
    profile_id: 'p2', profile_name: 'Work', org: 'bud', kind: 'approval', ref: 'apr-1', id: 'apr-1', ts: 1763632800000, role: 'writer',
    action: 'Bash', text: 'Approve Bash tool call?', request_id: 'apr-1', command: `echo a${RLO}b`, command_truncated: true, cwd: '/work/bud',
    input: { command: `echo a${RLO}b`, description: '[REDACTED] token' },
  },
  { profile_id: 'default', profile_name: 'Home & co', org: 'bud', kind: 'question', ref: 'q-1', id: 'q-1', ts: 1763632800000, role: 'lead', text: 'Which market?' },
  { profile_id: 'default', profile_name: 'Home & co', org: 'ops', kind: 'gate', ref: 'gate-1', id: 'gate-1', ts: 0, role: 'lead', name: 'publish', text: 'Publish the post' },
]
const listing = (items, errors = []) => JSON.stringify({ v: 1, items, errors })
const card = { id: 'w1', profile_id: 'p2', profile_name: 'Work', node_name: 'Review post', created_at: '2026-11-21 10:00:00', readonly_data: {}, editable_data: {} }

const open = () => {
  render(<HilScope><div>own queue</div></HilScope>)
  fireEvent.click(screen.getByRole('button', { name: 'All profiles' }))
}
const EMPTY = 'Nothing is waiting in any profile.'

describe('All profiles: organization items', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    WailsApp.HILPendingAll.mockResolvedValue('[]')
    WailsApp.HILOrgPendingAll.mockResolvedValue(listing([]))
  })
  afterEach(cleanup)

  it('asks only when the view is opened, and shows each item with its profile and org, read only', async () => {
    WailsApp.HILOrgPendingAll.mockResolvedValue(listing(orgItems))
    render(<HilScope><div>own queue</div></HilScope>)
    expect(WailsApp.HILOrgPendingAll).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'All profiles' }))

    await waitFor(() => expect(screen.getAllByTestId('hil-all-org-card')).toHaveLength(3))
    const [approval, question, gate] = screen.getAllByTestId('hil-all-org-card')
    expect(within(approval).getByTestId('hil-all-profile')).toHaveTextContent('Work')
    expect(within(approval).getByText('Org: bud')).toBeInTheDocument()
    expect(within(approval).getByText('Bash')).toBeInTheDocument()
    expect(within(question).getByTestId('hil-all-profile')).toHaveTextContent('Home & co')
    expect(within(question).getByText('Which market?')).toBeInTheDocument()
    expect(within(gate).getByText('Org: ops')).toBeInTheDocument()
    expect(within(gate).getByText('publish')).toBeInTheDocument()
    expect(within(gate).getByText('Publish the post')).toBeInTheDocument()

    // The command and the arguments with their safety rendering.
    const command = within(approval).getByTestId('approval-command')
    expect(within(command).getByTestId('approval-escape')).toHaveTextContent('\\u202E')
    expect(command.textContent).not.toContain(RLO)
    expect(within(command).getByText('Role folder: /work/bud')).toBeInTheDocument()
    expect(within(command).getByText(/Only the start of this command is known/)).toBeInTheDocument()
    expect(within(approval).getByTestId('approval-input')).toHaveTextContent('[REDACTED] token')
    expect(within(approval).getByText(/monomind hid part of these arguments/)).toBeInTheDocument()

    // Nothing can be decided or answered from here.
    expect(screen.queryByRole('button', { name: /approve|reject|deny|answer|submit/i })).toBeNull()
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.queryByText(EMPTY)).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('lists what could not be read beside what could', async () => {
    WailsApp.HILPendingAll.mockResolvedValue(JSON.stringify([card]))
    WailsApp.HILOrgPendingAll.mockResolvedValue(listing([orgItems[1]], [
      { profile_id: 'p2', profile_name: 'Work', org: 'broken', error: 'gates: not valid JSON' },
      { profile_id: 'p3', profile_name: 'Old', org: '', error: 'not a directory' },
    ]))
    open()
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Work · broken: gates: not valid JSON')
    expect(alert).toHaveTextContent('Old: not a directory')
    expect(screen.getAllByTestId('hil-all-org-card')).toHaveLength(1)
    expect(screen.getAllByTestId('hil-all-card')).toHaveLength(1)
  })

  it('keeps the other cards when the organization listing fails, and does not call it empty', async () => {
    WailsApp.HILPendingAll.mockResolvedValue(JSON.stringify([card]))
    WailsApp.HILOrgPendingAll.mockResolvedValue(JSON.stringify({ error: 'monomind is missing' }))
    open()
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not list organization items: monomind is missing')
    expect(await screen.findByTestId('hil-all-card')).toBeInTheDocument()

    cleanup()
    WailsApp.HILPendingAll.mockResolvedValue('[]')
    open()
    await screen.findByRole('alert')
    expect(screen.queryByText(EMPTY)).toBeNull()
  })

  it('shows organization items when the other listing fails', async () => {
    WailsApp.HILPendingAll.mockResolvedValue(JSON.stringify({ error: 'database is busy' }))
    WailsApp.HILOrgPendingAll.mockResolvedValue(listing([orgItems[1]]))
    open()
    expect(await screen.findByTestId('hil-all-org-card')).toBeInTheDocument()
    expect(await screen.findByRole('alert')).toHaveTextContent('database is busy')
  })

  it('shows the other cards while the organizations are still being read', async () => {
    WailsApp.HILPendingAll.mockResolvedValue(JSON.stringify([card]))
    WailsApp.HILOrgPendingAll.mockReturnValue(new Promise(() => {}))
    open()
    expect(await screen.findByTestId('hil-all-card')).toBeInTheDocument()
    expect(screen.getByText('Reading organizations…')).toBeInTheDocument()
  })

  it('says nothing is waiting only when both listings are empty', async () => {
    open()
    expect(await screen.findByText(EMPTY)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(WailsApp.HILOrgPendingAll).toHaveBeenCalledTimes(2))
    expect(WailsApp.HILPendingAll).toHaveBeenCalledTimes(2)
  })
})

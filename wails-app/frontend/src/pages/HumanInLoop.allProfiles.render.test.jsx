// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../i18n.js'
import HumanInLoop from './HumanInLoop.jsx'
import * as WailsApp from '../wailsjs/go/main/App'
import { api } from '../services/api.js'

vi.mock('../wailsjs/go/main/App', () => ({
  GetHILItems: vi.fn(),
  ApproveHIL: vi.fn(),
  RejectHIL: vi.fn(),
  GetDraftPersonMessages: vi.fn(),
  SendDraftPersonMessage: vi.fn(),
  RejectDraftPersonMessage: vi.fn(),
  HILPendingAll: vi.fn(),
}))

vi.mock('../services/api.js', () => ({
  api: { getPendingPeopleApprovals: vi.fn(), listOrgDesigns: vi.fn() },
  notify: vi.fn(),
}))

// One ref for the life of the page, as the real hook gives.
vi.mock('../lib/usePageVisible.js', () => {
  const visible = { current: true }
  return { usePageVisibleRef: () => visible, useVisibleCatchUp: () => {} }
})

const items = [
  {
    id: 's1', profile_id: 'default', profile_name: 'Home', node_name: 'send:email', created_at: '2026-11-20 10:00:00', kind: 'send',
    send: { action: 'email', channel: 'email', account: { handle: 'me@example.test' }, recipient: { name: 'ada@example.test' }, subject: 'Hello & welcome', text: 'Hi Ada' },
  },
  { id: 'w1', profile_id: 'p2', profile_name: 'Work', node_name: 'Review post', workflow_name: 'Publisher', created_at: '2026-11-21 10:00:00', readonly_data: {}, editable_data: { caption: 'A caption' } },
]

describe('Human in Loop, all profiles', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    WailsApp.GetHILItems.mockResolvedValue([])
    WailsApp.GetDraftPersonMessages.mockResolvedValue([])
    api.getPendingPeopleApprovals.mockResolvedValue([])
    api.listOrgDesigns.mockResolvedValue({ items: [] })
  })
  afterEach(cleanup)

  it('asks for every profile only when the view is opened, and names each card\'s profile', async () => {
    WailsApp.HILPendingAll.mockResolvedValue(JSON.stringify(items))
    render(<HumanInLoop />)
    expect(await screen.findByText('All caught up!')).toBeInTheDocument()
    expect(WailsApp.HILPendingAll).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'All profiles' }))
    await waitFor(() => expect(screen.getAllByTestId('hil-all-card')).toHaveLength(2))
    const [send, review] = screen.getAllByTestId('hil-all-card')
    expect(within(send).getByTestId('hil-all-profile')).toHaveTextContent('Home')
    expect(within(send).getByText('email via email to ada@example.test')).toBeInTheDocument()
    expect(within(send).getByText('Subject: Hello & welcome')).toBeInTheDocument()
    expect(within(send).getByText('Hi Ada')).toBeInTheDocument()
    expect(within(review).getByTestId('hil-all-profile')).toHaveTextContent('Work')
    expect(within(review).getByText('Review post')).toBeInTheDocument()
    expect(within(review).getByText('A caption')).toBeInTheDocument()
    // It only reads, and the profile's own queue is not shown beside it.
    expect(screen.queryByRole('button', { name: /approve|reject/i })).toBeNull()
    expect(screen.queryByText('All caught up!')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'This profile' }))
    expect(screen.getByText('All caught up!')).toBeInTheDocument()
    expect(screen.queryByTestId('hil-all-card')).toBeNull()
  })

  it('shows the CLI\'s refusal', async () => {
    WailsApp.HILPendingAll.mockResolvedValue(JSON.stringify({ error: 'only the user can do this' }))
    render(<HumanInLoop />)
    fireEvent.click(screen.getByRole('button', { name: 'All profiles' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('only the user can do this')
  })
})

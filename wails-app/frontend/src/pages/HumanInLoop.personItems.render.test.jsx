// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../i18n.js'
import HumanInLoop from './HumanInLoop.jsx'
import * as WailsApp from '../wailsjs/go/main/App'
import { api } from '../services/api.js'
import { PersonItemsProvider } from '../components/bubbles/PersonItemsContext.jsx'

vi.mock('../wailsjs/go/main/App', () => ({
  GetHILItems: vi.fn(), ApproveHIL: vi.fn(), RejectHIL: vi.fn(), GetDraftPersonMessages: vi.fn(),
  SendDraftPersonMessage: vi.fn(), RejectDraftPersonMessage: vi.fn(),
}))
vi.mock('../services/api.js', () => ({
  api: { getPendingPeopleApprovals: vi.fn(), listOrgDesigns: vi.fn(), getOrgQuestions: vi.fn(), getOrgApprovals: vi.fn(), getOrgGates: vi.fn() },
  notify: vi.fn(),
}))
vi.mock('../lib/usePageVisible.js', () => ({ usePageVisibleRef: () => ({ current: true }), useVisibleCatchUp: () => {} }))

const plan = { id: 4, kind: 'plan_approval', sessionId: 7, conversationId: 'c7', leadConversationId: 'c1', title: 'Port the importer', reasons: ['30 tasks, over 25'], taskCount: 30, budgetUsd: 10 }
const value = (items, extra = {}) => ({ items, loaded: true, refresh: vi.fn(), answer: vi.fn().mockResolvedValue({}), openAgent: vi.fn(), ...extra })

beforeEach(() => {
  vi.clearAllMocks()
  WailsApp.GetHILItems.mockResolvedValue([])
  WailsApp.GetDraftPersonMessages.mockResolvedValue([])
  api.getPendingPeopleApprovals.mockResolvedValue([])
  api.listOrgDesigns.mockResolvedValue({ items: [] })
})
afterEach(cleanup)

describe('HumanInLoop shows what waits for the person', () => {
  it('puts the person item first, counts it, and opens its agent', async () => {
    const v = value([plan])
    const onCount = vi.fn()
    render(<PersonItemsProvider value={v}><HumanInLoop embedded isOpen onPendingCountChange={onCount} /></PersonItemsProvider>)
    expect(await screen.findByTestId('person-inbox')).toHaveTextContent('Port the importer')
    expect(screen.queryByText(/All caught up/)).toBeNull()
    await waitFor(() => expect(onCount).toHaveBeenLastCalledWith(1))
    fireEvent.click(screen.getByRole('button', { name: /Open this agent/ }))
    expect(v.openAgent).toHaveBeenCalledWith(expect.objectContaining({ conversationId: 'c7' }))
    fireEvent.click(screen.getByRole('button', { name: /Approve/ }))
    expect(v.answer).toHaveBeenCalledWith('approve', expect.objectContaining({ sessionId: 7 }), undefined)
  })

  it('shows All caught up only when nothing waits', async () => {
    render(<PersonItemsProvider value={value([])}><HumanInLoop embedded isOpen /></PersonItemsProvider>)
    expect(await screen.findByText(/All caught up/)).toBeInTheDocument()
    expect(screen.queryByTestId('person-inbox')).toBeNull()
  })
})

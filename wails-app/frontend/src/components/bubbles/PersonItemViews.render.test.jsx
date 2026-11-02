// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import { useState } from 'react'
import '../../i18n.js'
import { PersonItemsProvider } from './PersonItemsContext.jsx'
import { PersonItemsInbox, PersonItemsRegion, PersonItemTray } from './PersonItemViews.jsx'

afterEach(cleanup)

const plan = (id, extra = {}) => ({ id, kind: 'plan_approval', sessionId: id * 10, conversationId: `c${id}`, leadConversationId: 'lead', title: `Plan ${id}`, reasons: ['30 tasks, over 25'], taskCount: 30, budgetUsd: 5, ...extra })
const wrap = (items, extra = {}) => ({ items, loaded: true, refresh: vi.fn(), answer: vi.fn().mockResolvedValue({}), openAgent: vi.fn(), ...extra })
const renderIn = (value, ui) => render(<PersonItemsProvider value={value}>{ui}</PersonItemsProvider>)

describe('the agent pane', () => {
  it('shows the card of its own session only', () => {
    renderIn(wrap([plan(1), plan(2)]), <PersonItemsRegion conversationId="c2" />)
    expect(screen.getByTestId('person-items-pane')).toBeInTheDocument()
    expect(screen.getByRole('group', { name: /Plan 2/ })).toBeInTheDocument()
    expect(screen.queryByRole('group', { name: /Plan 1/ })).toBeNull()
  })
  it('shows nothing for a chat with no item, and without a provider', () => {
    const { container } = renderIn(wrap([plan(1)]), <PersonItemsRegion conversationId="other" />)
    expect(container).toBeEmptyDOMElement()
    cleanup()
    expect(render(<PersonItemsRegion conversationId="c1" />).container).toBeEmptyDOMElement()
  })
  it('answers through the shared answer()', () => {
    const v = wrap([plan(1)])
    renderIn(v, <PersonItemsRegion conversationId="c1" />)
    fireEvent.click(screen.getByRole('button', { name: /Approve/ }))
    expect(v.answer).toHaveBeenCalledWith('approve', expect.objectContaining({ sessionId: 10 }), undefined)
  })
})

describe('the dock tray', () => {
  it('is an amber button with the count that opens the cards, and Escape closes it', () => {
    const v = wrap([plan(1), plan(2)])
    renderIn(v, <PersonItemTray />)
    const btn = screen.getByRole('button', { name: '2 items need you' })
    expect(btn).toHaveAttribute('aria-expanded', 'false')
    expect(btn).toHaveAttribute('data-fresh', 'true')
    fireEvent.click(btn)
    expect(btn).toHaveAttribute('aria-expanded', 'true')
    expect(btn).toHaveAttribute('data-fresh', 'false')
    expect(screen.getAllByTestId('person-item-card')).toHaveLength(2)
    fireEvent.click(screen.getAllByRole('button', { name: /Open this agent/ })[0])
    expect(v.openAgent).toHaveBeenCalledWith(expect.objectContaining({ conversationId: 'c1' }))
    fireEvent.keyDown(screen.getAllByTestId('person-item-card')[0], { key: 'Escape' })
    expect(screen.queryAllByTestId('person-item-card')).toHaveLength(0)
    expect(btn).toHaveFocus()
  })
  it('is absent with nothing waiting', () => {
    expect(renderIn(wrap([]), <PersonItemTray />).container).toBeEmptyDOMElement()
  })
})

describe('the inbox entry', () => {
  it('lists every open item at the top, with a mute toggle in its header and a way to the agent', () => {
    const v = wrap([plan(1), { id: 9, kind: 'parked_question', refId: 3, leadConversationId: 'conv-q' }])
    renderIn(v, <PersonItemsInbox />)
    const sec = screen.getByTestId('person-inbox')
    expect(sec).toHaveTextContent('Needs you (2)')
    expect(screen.getAllByRole('button', { name: 'Mute the chime' })).toHaveLength(1)
    expect(screen.getByRole('group', { name: /Plan 1/ })).toBeInTheDocument()
    expect(sec).toHaveTextContent('parked_question')
    const open = screen.getAllByRole('button', { name: /Open this agent/ })
    expect(open).toHaveLength(2)
    fireEvent.click(open[1])
    expect(v.openAgent).toHaveBeenCalledWith(expect.objectContaining({ conversationId: 'conv-q' }))
  })
  it('is absent with nothing waiting', () => {
    expect(renderIn(wrap([]), <PersonItemsInbox />).container).toBeEmptyDOMElement()
  })
})

// An answered card leaves the page with the focus that was on its button: it goes back to the composer or the tray.
function Live({ initial, fail = false, children }) {
  const [items, setItems] = useState(initial)
  const answer = async (verb, item) => {
    if (fail) return { error: 'refused' }
    setItems(list => list.filter(i => i.id !== item.id))
    return {}
  }
  return <PersonItemsProvider value={wrap(items, { answer })}>{children}</PersonItemsProvider>
}

describe('focus after an answer', () => {
  it('goes to the pane composer when the plan card is approved', async () => {
    render(<Live initial={[plan(1)]}><div data-testid="bubble-overlay" role="dialog"><PersonItemsRegion conversationId="c1" /><textarea aria-label="composer" /></div></Live>)
    const approve = screen.getByRole('button', { name: /Approve/ })
    approve.focus()
    fireEvent.click(approve)
    await waitFor(() => expect(screen.queryByTestId('person-items-pane')).toBeNull())
    expect(screen.getByLabelText('composer')).toHaveFocus()
  })
  it('goes back to the tray button while other items still wait', async () => {
    render(<Live initial={[plan(1), plan(2)]}><PersonItemTray /></Live>)
    const btn = screen.getByRole('button', { name: '2 items need you' })
    fireEvent.click(btn)
    const approve = screen.getAllByRole('button', { name: /Approve/ })[0]
    approve.focus()
    fireEvent.click(approve)
    await waitFor(() => expect(screen.getAllByTestId('person-item-card')).toHaveLength(1))
    expect(screen.getByRole('button', { name: '1 item needs you' })).toHaveFocus()
  })
  it('leaves the focus alone when the answer is refused', async () => {
    render(<Live initial={[plan(1)]} fail><div data-testid="bubble-overlay" role="dialog"><PersonItemsRegion conversationId="c1" /><textarea aria-label="composer" /></div></Live>)
    const approve = screen.getByRole('button', { name: /Approve/ })
    approve.focus()
    fireEvent.click(approve)
    await screen.findByRole('alert')
    expect(screen.getByLabelText('composer')).not.toHaveFocus()
    expect(screen.getByTestId('person-item-card')).toBeInTheDocument()
  })
})

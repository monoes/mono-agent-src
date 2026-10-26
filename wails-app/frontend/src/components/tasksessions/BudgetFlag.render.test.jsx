// @vitest-environment jsdom
// Near budget and Budget reached are said in words and with an icon on the tab, the header and the mission strip: colour is never the only signal.
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, render, screen, within } from '@testing-library/react'
import '../../i18n.js'

const live = vi.hoisted(() => ({ session: null }))
vi.mock('../bubbles/mission/useMissionSession.js', () => ({ useConversationSession: () => ({ session: live.session, board: null, reload: async () => {} }) }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: {} }))
import SessionSwitcher from './SessionSwitcher.jsx'
import SessionHeader from './SessionHeader.jsx'
import MissionStrip from '../bubbles/mission/MissionStrip.jsx'
import { budgetLevel } from '../../lib/taskSessionModel.js'

const mk = (spent, extra = {}) => ({ id: 1, title: 'M', state: 'running', archived: false, budget_usd: 10, spent_usd: spent, counts: { done: 1, total: 2 }, conversation_id: 'c1', ...extra })
afterEach(cleanup)

describe('budgetLevel', () => {
  it('is empty below 90 percent and with no budget, near from 90, over at the budget', () => {
    expect([mk(8.9), mk(9), mk(10), mk(12), mk(5, { budget_usd: 0 })].map(budgetLevel)).toEqual(['', 'near', 'over', 'over', ''])
  })
})

describe('budget flag', () => {
  it('shows no flag under 90 percent', () => {
    render(<SessionSwitcher sessions={[mk(1)]} selectedId={null} onSelect={() => {}} />)
    expect(screen.queryByTestId('ts-budget-flag')).not.toBeInTheDocument()
  })
  it.each([[9.5, 'Near budget'], [10, 'Budget reached']])('names it on the tab at %s spent', (spent, text) => {
    render(<SessionSwitcher sessions={[mk(spent)]} selectedId={null} onSelect={() => {}} />)
    const flag = within(screen.getByRole('tab', { name: /M/ })).getByTestId('ts-budget-flag')
    expect(flag).toHaveTextContent(text)
    expect(flag).toHaveAttribute('aria-label', text)
    expect(flag).toHaveAttribute('title', text)
    expect(flag.querySelector('svg')).toBeTruthy()
  })
  it.each([[9.5, 'Near budget'], [11, 'Budget reached']])('names it in the session header at %s spent', (spent, text) => {
    render(<SessionHeader session={mk(spent)} view="board" onView={() => {}} onAct={() => {}} onBudget={() => {}} />)
    const flag = screen.getByTestId('ts-budget-flag')
    expect(flag).toHaveTextContent(text)
    expect(flag).toHaveAttribute('aria-label', text)
    expect(flag.querySelector('svg')).toBeTruthy()
  })
  it.each([[9.5, 'Near budget'], [10, 'Budget reached']])('names it in the mission strip at %s spent', (spent, text) => {
    live.session = mk(spent)
    render(<MissionStrip conversationId="c1" />)
    const flag = within(screen.getByTestId('mission-strip')).getByTestId('ts-budget-flag')
    expect(flag).toHaveTextContent(text)
    expect(flag).toHaveAttribute('title', text)
  })
})

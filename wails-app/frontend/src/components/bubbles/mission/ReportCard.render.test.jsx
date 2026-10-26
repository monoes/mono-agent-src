// @vitest-environment jsdom
// The report card shows what the engine really sends: mission.report carries `report` (markdown), not `summary`.
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, render, screen } from '@testing-library/react'
import '../../../i18n.js'

vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn() }, notify: vi.fn() }))
vi.mock('../../../services/taskSessions.js', () => ({ sessionsApi: {} }))
import { ReportCard } from './MissionCards.jsx'

afterEach(cleanup)

describe('ReportCard', () => {
  it('renders the report markdown of a real mission.report payload', () => {
    const payload = { sessionId: 7, title: 'Add CSV export', state: 'done', taskCount: 4, done: 4, verified: 4, unfinished: [], artifacts: 3, spentUsd: 2.9, budgetUsd: 5, report: '## Summary\n\nExported the orders list as CSV.\n\n- streaming for large sets' }
    render(<ReportCard item={{ type: 'report', id: 'r', payload }} onNavigate={() => {}} />)
    expect(screen.getByRole('heading', { name: 'Summary' })).toBeInTheDocument()
    expect(screen.getByText('Exported the orders list as CSV.')).toBeInTheDocument()
    expect(screen.getByText('streaming for large sets')).toBeInTheDocument()
  })
})

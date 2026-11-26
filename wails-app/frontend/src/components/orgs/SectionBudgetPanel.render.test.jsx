// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, within, cleanup, waitFor, act } from '@testing-library/react'
import { SectionBudgetView } from './SectionBudgetPanel.jsx'
import SectionBudgetPanel from './SectionBudgetPanel.jsx'
import { SectionBoxes } from '../orgdesigner/SectionLayer.jsx'
import { bySection } from '../orgdesigner/budgetModel.js'
import recorded from '../orgdesigner/__fixtures__/org-budget-recorded.json'
import { api, onOrgEvent } from '../../services/api.js'
import '../../i18n.js'

vi.mock('../../services/api.js', () => ({
  api: { getOrgBudget: vi.fn(), getOrgEstimate: vi.fn() },
  onOrgEvent: vi.fn(() => () => {}),
}))

// The recorded run (monomind 2.24.1, budget-*): drafting and review both ended
// soft-closed. `report` is review as it stood at its recorded warning event.
const report = { ...recorded, sections: recorded.sections.map(s => (s.name === 'review'
  ? { ...s, spent_usd: 0.034, remaining_usd: 0.006, fraction: 0.85, state: 'warn', soft_closed: false, closed_at: 0, closed_roles: [] } : s)) }

afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('SectionBudgetView (org status panel)', () => {
  it('shows each section against its allocation, role caps, the soft closure and totals (recorded run)', () => {
    render(<SectionBudgetView report={recorded} />)
    const d = screen.getByTestId('budget-section-drafting')
    expect(within(d).getByText('$0.07 / $0.05')).toBeInTheDocument()
    expect(within(d).getByTestId('budget-badge')).toHaveTextContent('soft-closed')
    expect(within(d).getByTestId('role-cap-writer')).toHaveTextContent('writer $0.07/$0.05')
    expect(within(d).getByTestId('soft-closure')).toHaveTextContent('closed: writer')
    const r = screen.getByTestId('budget-section-review')
    expect(within(r).getByText('$0.05 / $0.04')).toBeInTheDocument()
    expect(within(r).getByTestId('budget-badge')).toHaveTextContent('soft-closed')
    expect(screen.getByTestId('budget-reserve-reserve')).toHaveTextContent('$0.02 / $0.06')
    expect(screen.getByTestId('budget-total')).toHaveTextContent('org total $0.14 / $0.15')
    expect(screen.queryByText(/cost incomplete/)).toBeNull()
  })
  it('warns before the cap with what is left', () => {
    render(<SectionBudgetView report={report} />)
    const r = screen.getByTestId('budget-section-review')
    expect(within(r).getByText('$0.03 / $0.04')).toBeInTheDocument()
    expect(within(r).getByTestId('budget-badge')).toHaveTextContent('$0.01 left')
    expect(within(r).queryByTestId('soft-closure')).toBeNull()
  })
  it('says so when a usage event had no cost, and renders nothing without budgets', () => {
    const unknown = { ...report, cost_complete: false }
    render(<SectionBudgetView report={unknown} />)
    expect(screen.getByText(/cost incomplete: lower bound/)).toBeInTheDocument()
    cleanup()
    const none = { sections: [{ kind: 'section', name: 'a', state: 'unallocated', spent_usd: 0 }], total: { state: 'unallocated' } }
    const { container } = render(<SectionBudgetView report={none} />)
    expect(container).toBeEmptyDOMElement()
  })
})

const ESTIMATE = {
  text: 'Cost estimate\n  (static)\n  ⚠ stale rates: no live provider pricing lookup — hardcoded table (edit ~/.monomind/rates.json to override)\n  Total estimate:              ~$1.80',
  stale_rates: '⚠ stale rates: no live provider pricing lookup — hardcoded table (edit ~/.monomind/rates.json to override)',
}

describe('stale rates', () => {
  it('shows monomind\'s own line verbatim in the panel and a marker in the section header', () => {
    render(<SectionBudgetView report={report} estimate={ESTIMATE} />)
    expect(screen.getByTestId('stale-rates')).toHaveTextContent(ESTIMATE.stale_rates)
    expect(screen.getByTestId('stale-rates')).toHaveAttribute('title', ESTIMATE.text)
    cleanup()
    const rects = [{ name: 'drafting', x: 0, y: 0, w: 300, h: 200, color: '#38bdf8' }]
    render(<SectionBoxes rects={rects} sections={[{ name: 'drafting', leadId: 'writer' }]} readOnly budgets={bySection(report)} staleRates={ESTIMATE.stale_rates} />)
    expect(screen.getByTestId('section-stale-rates-drafting')).toHaveAttribute('aria-label', 'stale rates')
    expect(screen.getByTestId('section-budget-drafting')).toHaveAttribute('title', expect.stringContaining('stale rates: no live provider'))
  })
  it('shows nothing when the estimate is unavailable', () => {
    render(<SectionBudgetView report={report} estimate={null} />)
    expect(screen.queryByTestId('stale-rates')).toBeNull()
  })
  it('the panel fetches the estimate once', async () => {
    api.getOrgBudget.mockResolvedValue(report)
    api.getOrgEstimate.mockResolvedValue(ESTIMATE)
    render(<SectionBudgetPanel orgName="bud" run="r1" />)
    await waitFor(() => expect(screen.getByTestId('stale-rates')).toBeInTheDocument())
    expect(api.getOrgEstimate).toHaveBeenCalledTimes(1)
    expect(api.getOrgEstimate).toHaveBeenCalledWith('bud')
  })
})

describe('SectionBudgetPanel fetch', () => {
  it('fetches the run and refetches on a usage event while live', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let emit
    onOrgEvent.mockImplementation((cb) => { emit = cb; return () => {} })
    api.getOrgBudget.mockResolvedValue(report)
    render(<SectionBudgetPanel orgName="bud" live />)
    await waitFor(() => expect(screen.getByTestId('section-budget-panel')).toBeInTheDocument())
    expect(api.getOrgBudget).toHaveBeenCalledWith('bud', '')
    act(() => emit({ orgName: 'bud', event: { type: 'usage' } }))
    act(() => emit({ orgName: 'other', event: { type: 'usage' } }))
    act(() => emit({ orgName: 'bud', event: { type: 'status' } }))
    await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
    expect(api.getOrgBudget).toHaveBeenCalledTimes(2)
    vi.useRealTimers()
  })
})

describe('section header on the canvas', () => {
  const rects = [{ name: 'drafting', x: 0, y: 0, w: 300, h: 200, color: '#38bdf8' }, { name: 'review', x: 400, y: 0, w: 300, h: 200, color: '#a78bfa' }]
  const sections = [{ name: 'drafting', leadId: 'writer', budgetUsd: 0.05 }, { name: 'review', leadId: 'checker', budgetUsd: 0.04 }]
  it('shows live spend, the soft-closed badge and the warning before the cap', () => {
    render(<SectionBoxes rects={rects} sections={sections} selectedName={null} readOnly budgets={bySection(report)} />)
    expect(screen.getByTestId('section-budget-drafting')).toHaveTextContent('$0.07/$0.05')
    expect(screen.getByTestId('section-budget-badge-drafting')).toHaveTextContent('soft-closed')
    expect(screen.getByTestId('section-budget-review')).toHaveTextContent('$0.03/$0.04')
    expect(screen.getByTestId('section-budget-badge-review')).toHaveTextContent('$0.01 left')
  })
  it('keeps the declared budget in the design view', () => {
    render(<SectionBoxes rects={rects} sections={sections} selectedName={null} readOnly={false} />)
    expect(screen.getByTestId('section-header-review')).toHaveTextContent('$0.04')
    expect(screen.queryByTestId('section-budget-review')).toBeNull()
  })
})

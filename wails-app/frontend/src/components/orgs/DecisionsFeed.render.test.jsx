// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, within } from '@testing-library/react'
import DecisionsFeed, { jevSummary, jevDistribution } from './DecisionsFeed.jsx'
import { api } from '../../services/api.js'

vi.mock('../../services/api.js', () => ({
  api: { listOrgDecisionLog: vi.fn() },
  notify: vi.fn(),
}))

// Rows as `monoagentcli org autonomy decisions` prints them.
const DECISIONS = [
  {
    id: 'd-jev', class: 'tool:WebFetch', tier: 'consequential', level: 'mid', resolver: 'jev:jev-1.13', verdict: 'approved',
    rationale: 'jev jev-1.13: approve p=0.93 (approve 0.93, deny 0.05, escalate 0.02)', cost_usd: 0.0001, created_at: '2026-09-25T10:02:00Z',
    confidence: 0.9, probabilities: { approve: 0.93, deny: 0.05, escalate: 0.02 },
    jev: { decided: true, verdict: 'approve', p: 0.93, threshold: null },
  },
  {
    id: 'd-fb', class: 'grant:publish_post', tier: 'consequential', level: 'mid', resolver: 'model:claude-fable-5-1', verdict: 'denied',
    rationale: 'jev jev-1.13: approve p=0.62 below 0.80 (approve 0.62, deny 0.38); outbound post needs review', cost_usd: 0.004, created_at: '2026-09-25T10:01:00Z',
    confidence: 0.5, probabilities: { approve: 0.62, deny: 0.38 },
    jev: { decided: false, verdict: 'approve', p: 0.62, threshold: 0.8 },
  },
  {
    id: 'd-model', class: 'org_complete', tier: 'consequential', level: 'mid', resolver: 'model:claude-fable-5-1', verdict: 'approved',
    rationale: 'Goal met.', cost_usd: 0.003, created_at: '2026-09-25T10:00:00Z',
  },
]

beforeEach(() => {
  vi.clearAllMocks()
  api.listOrgDecisionLog.mockResolvedValue({ v: 1, org: 'growth', decisions: DECISIONS })
})
afterEach(() => cleanup())

describe('jevSummary', () => {
  it('reads the CLI jev view', () => {
    expect(jevSummary(DECISIONS[0])).toBe('Jev p=0.93')
    expect(jevSummary(DECISIONS[1])).toBe('model decided (Jev p=0.62 below 0.8)')
    expect(jevSummary({ jev: { decided: false, p: 0.4, threshold: null } })).toBe('model decided (Jev p=0.40)')
    expect(jevSummary(DECISIONS[2])).toBeNull()
  })
  it('orders the distribution highest first', () => {
    expect(jevDistribution(DECISIONS[0].probabilities).map(x => x.verdict)).toEqual(['approve', 'deny', 'escalate'])
  })
})

describe('DecisionsFeed', () => {
  it('shows a Jev chip, the model fallback, and nothing for plain model rows', async () => {
    render(<DecisionsFeed orgName="growth" />)
    const rows = await screen.findAllByTestId('decision-row')
    expect(rows).toHaveLength(3)
    const [jevRow, fbRow, modelRow] = rows
    expect(within(jevRow).getByTestId('jev-chip')).toHaveTextContent('Jev p=0.93')
    expect(within(jevRow).getByTestId('jev-chip')).toHaveAttribute('title', 'Jev: approve 0.93, deny 0.05, escalate 0.02')
    expect(within(fbRow).getByTestId('jev-chip')).toHaveTextContent('model decided (Jev p=0.62 below 0.8)')
    expect(within(modelRow).queryByTestId('jev-chip')).not.toBeInTheDocument()
  })

  it('expands the per-verdict distribution', async () => {
    render(<DecisionsFeed orgName="growth" />)
    const [jevRow] = await screen.findAllByTestId('decision-row')
    expect(within(jevRow).queryByTestId('jev-distribution')).not.toBeInTheDocument()
    fireEvent.click(within(jevRow).getByTestId('jev-chip'))
    const dist = within(jevRow).getByTestId('jev-distribution')
    expect(dist).toHaveTextContent('approve')
    expect(dist).toHaveTextContent('0.05')
    expect(dist).toHaveTextContent('escalate')
    expect(within(jevRow).getByTestId('jev-chip')).toHaveAttribute('aria-expanded', 'true')
  })
})

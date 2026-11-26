// @vitest-environment jsdom
// #1766: a signed org that never ran shows monomind's pre-run estimate on the
// Overview (`org estimate`), also right after it is signed there.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor, cleanup, act } from '@testing-library/react'
import '../../i18n.js'
import OrgTabBody from './OrgTabBody.jsx'
import { reviewAndSign } from './OrgSignatureBanner.jsx'
import { api } from '../../services/api.js'

vi.mock('../../services/api.js', () => ({
  api: { getOrgEstimate: vi.fn(), validateOrgReport: vi.fn(), orgSignatureReview: vi.fn(), orgSign: vi.fn() },
  notify: vi.fn(), onOrgEvent: () => () => {},
}))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: vi.fn(() => Promise.resolve(true)) }))

const estimate = {
  text: 'Cost estimate\n  lead   haiku  ~$0.60\n  stale rates: priced 90 days ago\nTotal estimate: ~$1.80',
  stale_rates: 'stale rates: priced 90 days ago', total_usd: 1.8,
}
const props = { selected: 'bud', tab: 'overview', tabLoading: false, events: [], runsList: [], selectedRun: null, runDetail: null }
const body = (status, extra = {}) => render(<OrgTabBody {...props} data={{ overview: { name: 'bud', status } }} {...extra} />)

beforeEach(() => {
  vi.clearAllMocks()
  api.validateOrgReport.mockResolvedValue({})
  api.getOrgEstimate.mockResolvedValue(estimate)
})
afterEach(cleanup)

describe('the estimate before the first run', () => {
  it('shows monomind\'s estimate for an org that never ran', async () => {
    body('never run')
    const panel = await screen.findByTestId('org-estimate')
    expect(panel).toHaveTextContent('Total estimate: ~$1.80')
    expect(panel).toHaveTextContent('stale rates: priced 90 days ago')
    expect(api.getOrgEstimate).toHaveBeenCalledWith('bud')
  })

  // `org estimate` starts `org run` up to its estimate, so it is asked only for
  // an org that is known never to have run.
  it.each(['running', 'stopped', undefined])('asks for no estimate when the status is %s', async (status) => {
    body(status)
    await act(async () => {})
    expect(api.getOrgEstimate).not.toHaveBeenCalled()
    expect(screen.queryByTestId('org-estimate')).toBeNull()
  })

  it('shows nothing while the CLI refuses, and reads it again after the org is signed', async () => {
    api.getOrgEstimate.mockResolvedValueOnce({ error: 'cost estimate unavailable: org bud is not signed' })
    body('never run')
    await waitFor(() => expect(api.getOrgEstimate).toHaveBeenCalledTimes(1))
    expect(screen.queryByTestId('org-estimate')).toBeNull()
    api.orgSignatureReview.mockResolvedValue({ hash: 'h1', review: 'roles' })
    api.orgSign.mockResolvedValue({ ok: true })
    await act(() => reviewAndSign('bud'))
    expect(await screen.findByTestId('org-estimate')).toHaveTextContent('Total estimate')
    expect(api.getOrgEstimate).toHaveBeenCalledTimes(2)
  })
})

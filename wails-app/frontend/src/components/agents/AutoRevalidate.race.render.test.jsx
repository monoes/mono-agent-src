// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'
import '../../i18n.js'

const { api } = vi.hoisted(() => ({
  api: { agentRosterAutoRevalidate: vi.fn(), setAgentRosterAutoRevalidate: vi.fn() },
}))
vi.mock('../../services/api.js', () => ({ api }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: () => Promise.resolve(true) }))

import AutoRevalidate from './AutoRevalidate.jsx'

const off = {
  enabled: false, max_runtimes_per_day: 1, max_models_per_run: 3, quiet_period: '15m0s', daemon_running: true,
  daily_max_usd: 0.15, priciest_model_usd: 0.05, cost_known: true,
  state: { runtimes_today: 0, spent_today_usd: 0, last_run_at: '0001-01-01T00:00:00Z', next_eligible_at: '0001-01-01T00:00:00Z' },
  next: { runtime: 'codex', targets: [{ model: 'a' }], est_cost_usd: 0.002, unknown_cost: 0 },
}

beforeEach(() => { vi.clearAllMocks() })
afterEach(cleanup)

// A reload that started before the toggle (the roster changed while the user was
// confirming) can answer after it with the old "off" state: it must not undo the toggle.
describe('AutoRevalidate ordering', () => {
  it('a reload that answers after the toggle does not put the old state back', async () => {
    let lateReload
    api.agentRosterAutoRevalidate
      .mockResolvedValueOnce(off)
      .mockImplementationOnce(() => new Promise(res => { lateReload = res }))
    api.setAgentRosterAutoRevalidate.mockImplementation(async on => ({ ...off, enabled: on }))

    const { rerender } = render(<AutoRevalidate refreshKey={1} />)
    const box = await screen.findByLabelText(/Re-check stale models automatically/)
    rerender(<AutoRevalidate refreshKey={2} />) // starts the second load, left pending
    await waitFor(() => expect(api.agentRosterAutoRevalidate).toHaveBeenCalledTimes(2))

    fireEvent.click(box)
    await waitFor(() => expect(api.setAgentRosterAutoRevalidate).toHaveBeenCalledWith(true))
    await waitFor(() => expect(box).toBeChecked())

    await act(async () => { lateReload(off) }) // the old answer arrives last
    expect(box).toBeChecked()
  })
})

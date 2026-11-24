// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor, act } from '@testing-library/react'

// The page's run and save paths (R21-06: a thrown run says why; R21-07: the
// save message timer is cleared). Every binding answers empty unless the test
// overrides it.
const h = vi.hoisted(() => ({
  notify: vi.fn(),
  save: vi.fn(),
  runWithInput: vi.fn(),
}))
const WF = { id: 'w1', name: 'Flow', is_active: true, nodes: [{ id: 'n1', node_type: 'core.noop', name: 'Noop', position_x: 10, position_y: 10, config: {} }], connections: [] }
vi.mock('../wailsjs/go/main/App', () => new Proxy({
  GetWorkflow: () => Promise.resolve(WF),
  SaveWorkflow: (...a) => h.save(...a),
  SetWorkflowActive: () => Promise.resolve(),
}, { get: (t, k) => (k in t ? t[k] : k === '__esModule' ? false : k === 'then' ? undefined : () => Promise.resolve(null)), has: (t, k) => k in t || true }))
vi.mock('../services/api.js', () => ({
  api: new Proxy({ runWorkflowWithInput: (...a) => h.runWithInput(...a) }, {
    get: (t, k) => (k in t ? t[k] : () => Promise.resolve(k === 'getExecutionDetail' ? null : [])),
  }),
  notify: (...a) => h.notify(...a),
  reportRunFailure: vi.fn(),
  subscribeEvent: () => () => {},
}))

import NodeRunner from './NodeRunner.jsx'

beforeEach(() => { localStorage.clear(); localStorage.setItem('nr2-last-wf-id', 'w1'); h.save.mockResolvedValue({ id: 'w1' }) })
afterEach(() => { cleanup(); vi.clearAllMocks(); vi.useRealTimers() })

describe('NodeRunner run and save', () => {
  it('a thrown run tells the user why and frees the Run button (R21-06)', async () => {
    h.runWithInput.mockRejectedValue(new Error('engine exploded'))
    render(<NodeRunner onNavigate={() => {}} navData={null} />)
    const run = await screen.findByTitle('Run all nodes')
    await waitFor(() => expect(run).toBeEnabled())
    fireEvent.click(run)
    await waitFor(() => expect(h.notify).toHaveBeenCalledWith('run workflow', 'engine exploded'))
    expect(await screen.findByTitle('Run all nodes')).toBeEnabled()
  })

  it('a second save inside 3s keeps its message: the first timer is cleared (R21-07)', async () => {
    render(<NodeRunner onNavigate={() => {}} navData={null} />)
    await screen.findByTitle('Run all nodes')
    vi.useFakeTimers()
    const save = async () => {
      fireEvent.click(screen.getByTitle('Save workflow'))
      await act(async () => {})
      fireEvent.click(screen.getByRole('button', { name: /^save$/i }))
      await act(async () => {})
    }
    await save()
    expect(screen.getByText('Saved')).toBeInTheDocument()
    await act(async () => { vi.advanceTimersByTime(2000) })
    await save()
    await act(async () => { vi.advanceTimersByTime(1500) }) // 3.5s after the first save
    expect(screen.getByText('Saved')).toBeInTheDocument()
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(screen.queryByText('Saved')).toBeNull()
  })
})

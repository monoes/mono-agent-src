// @vitest-environment jsdom
// W5 r21-D-06: deleting a workflow clears the run input remembered for it.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import '../i18n.js'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

const WF = { id: 'w1', name: 'Flow', is_active: true, nodes: [{ id: 'n1', node_type: 'core.noop', name: 'Noop', position_x: 10, position_y: 10, config: {} }], connections: [] }
const del = vi.hoisted(() => vi.fn(() => Promise.resolve()))
vi.mock('../wailsjs/go/main/App', () => new Proxy({
  GetWorkflow: () => Promise.resolve(WF),
  ListWorkflows: () => Promise.resolve([{ id: 'w1', name: 'Flow', node_count: 1 }]),
  DeleteWorkflow: (...a) => del(...a),
}, { get: (t, k) => (k in t ? t[k] : k === '__esModule' ? false : k === 'then' ? undefined : () => Promise.resolve(null)), has: () => true }))
vi.mock('../services/api.js', () => ({
  api: new Proxy({}, { get: () => () => Promise.resolve([]) }),
  notify: vi.fn(),
  reportRunFailure: vi.fn(),
  subscribeEvent: () => () => {},
}))
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: () => Promise.resolve(true) }))

import NodeRunner from './NodeRunner.jsx'

const KEY = 'monoagent:wf-trigger-input:w1'
beforeEach(() => { localStorage.clear(); localStorage.setItem(KEY, '{"secret":"x"}') })
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('NodeRunner delete', () => {
  it('drops the remembered trigger input of the deleted workflow', async () => {
    render(<NodeRunner onNavigate={() => {}} navData={null} />)
    fireEvent.click(await screen.findByTitle('Open saved workflow'))
    fireEvent.click(await screen.findByTitle('Delete'))
    await waitFor(() => expect(del).toHaveBeenCalledWith('w1'))
    await waitFor(() => expect(localStorage.getItem(KEY)).toBeNull())
  })
})

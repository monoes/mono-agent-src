// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within, act } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import RoleNode from './RoleNode.jsx'
import OrgCanvas, { liveEdgesFor } from './OrgCanvas.jsx'
import OrgDesigner from './OrgDesigner.jsx'
import { replay, initialState, applyEvent } from './orgActivity.js'
import { api, onOrgEvent } from '../../services/api.js'

vi.mock('../../services/api.js', () => ({
  api: {
    getOrgDesign: vi.fn(),
    listOrgAutomations: vi.fn(),
    listOrgGrants: vi.fn(),
    getOrgAutonomy: vi.fn(),
    getDaemonStatus: vi.fn(),
    getOrgLogs: vi.fn(),
    getOrgReport: vi.fn(),
    addAutomationRole: vi.fn(),
    removeAutomationRole: vi.fn(),
    saveOrgLayout: vi.fn(),
    setOrgGrant: vi.fn(),
    getEffectiveTools: vi.fn(),
    listUnassignedAutomations: vi.fn(),
    chooseInstructionsFile: vi.fn(),
  },
  notify: vi.fn(),
  onOrgEvent: vi.fn(() => () => {}),
  onOrgDesignUpdated: vi.fn(() => () => {}),
}))
vi.mock('./roleIcons.js', () => ({
  loadIconManifest: vi.fn(() => Promise.resolve([])),
  suggestIcon: vi.fn(() => Promise.resolve('coder')),
  iconUrl: vi.fn(() => ''),
  CATEGORY_TYPE: {},
  CAT_COLOR: {},
  getRecentIcons: vi.fn(() => []),
  pushRecentIcon: vi.fn(),
}))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: vi.fn(() => Promise.resolve(true)) }))

// jsdom has no ResizeObserver; this one lets a test resize the canvas box.
const observers = []
class FakeResizeObserver {
  constructor(cb) { this.cb = cb; this.targets = []; observers.push(this) }
  observe(el) { this.targets.push(el) }
  disconnect() { this.targets = [] }
  fire(width, height) { this.cb(this.targets.map(target => ({ target, contentRect: { width, height } }))) }
}

function fixture(name) {
  return readFileSync(join(process.cwd(), 'src/components/orgdesigner/__fixtures__', `${name}.jsonl`), 'utf8')
    .split('\n').filter(Boolean).map(l => JSON.parse(l))
}

const heraldNodes = [
  { id: 'editor', title: 'Editor', type: 'boss', parentId: null, x: 200, y: 0, rest: {} },
  { id: 'reviewer', title: 'Reviewer', type: 'specialist', parentId: 'editor', x: 0, y: 160, rest: {} },
  { id: 'writer', title: 'Writer', type: 'specialist', parentId: 'editor', x: 200, y: 160, rest: {} },
  { id: 'judge', title: 'Judge', type: 'specialist', parentId: 'editor', x: 400, y: 160, rest: {} },
]

beforeEach(() => {
  vi.clearAllMocks()
  observers.length = 0
  globalThis.ResizeObserver = FakeResizeObserver
  document.elementFromPoint = vi.fn(() => null)
  try { localStorage.clear() } catch { /* ignore */ }
})
afterEach(() => {
  cleanup()
  delete globalThis.ResizeObserver
})

describe('RoleNode', () => {
  it('renders an automation role with a workflow icon, chip, and engine-offline warning', () => {
    const node = { id: 'bot', title: 'Publisher', parentId: 'lead', rest: { kind: 'endpoint' } }
    const { container } = render(<RoleNode node={node} isRoot={false} engineOffline />)
    expect(container.querySelector('[data-kind="automation"]')).toBeInTheDocument()
    expect(screen.getByLabelText('automation')).toBeInTheDocument()
    expect(screen.getByText('automation')).toBeInTheDocument()
    expect(screen.getByTestId('engine-offline')).toHaveTextContent('engine offline')
  })

  it('does not warn about the engine for agent roles', () => {
    render(<RoleNode node={{ id: 'lead', title: 'Lead', parentId: null, rest: {} }} isRoot engineOffline />)
    expect(screen.queryByTestId('engine-offline')).not.toBeInTheDocument()
  })

  it('shows live status, a padlock for a pending gate, and asset and approval markers', () => {
    const live = {
      status: 'blocked', pendingGate: { gateId: 'g1', name: 'publish' }, assetCount: 2,
      assets: [{ path: '<root>/a.md', ts: 1 }], pendingApprovals: 1, pendingQuestions: 0,
    }
    const onStartMove = vi.fn()
    const { container } = render(<RoleNode node={{ id: 'judge', title: 'Judge', parentId: 'editor', rest: {} }} live={live} readOnly onStartMove={onStartMove} onSelect={vi.fn()} />)
    expect(container.querySelector('[data-live-status="blocked"]')).toBeInTheDocument()
    expect(screen.getByTestId('live-gate')).toHaveAttribute('title', 'Gate pending: publish')
    expect(screen.getByTestId('live-asset')).toHaveTextContent('2')
    expect(screen.getByTestId('live-approvals')).toHaveTextContent('1')
    expect(screen.queryByTitle('Delete role')).not.toBeInTheDocument()
    fireEvent.mouseDown(container.querySelector('[data-live-status]'))
    expect(onStartMove).not.toHaveBeenCalled()
  })
})

describe('OrgCanvas live view', () => {
  it('recolours cards and draws message edges from a replayed bus', () => {
    const state = replay(fixture('herald-rehearsal'), heraldNodes, { org: 'herald' })
    render(<OrgCanvas nodes={heraldNodes} liveState={state} liveNow={state.lastTs} readOnly />)
    const canvas = screen.getByTestId('org-canvas')
    expect(canvas).toHaveAttribute('data-mode', 'live')
    const judge = canvas.querySelector('[data-od-node-id="judge"]')
    expect(within(judge).getByTestId('live-status')).toHaveTextContent('idle')
    expect(within(judge).getByTestId('live-gate')).toBeInTheDocument()
    expect(canvas.querySelector('[data-od-node-id="writer"] [data-live-status="offline"]')).toBeInTheDocument()
    const edges = screen.getAllByTestId('live-edge')
    const pairs = edges.map(e => `${e.getAttribute('data-from')}->${e.getAttribute('data-to')}`).sort()
    expect(pairs).toEqual(['editor->judge', 'editor->reviewer', 'judge->editor', 'reviewer->editor'])
  })

  it('marks only edges within the recent window as animated', () => {
    let s = initialState(heraldNodes, { org: 'herald' })
    s = applyEvent(s, { id: '1', ts: 1_000, type: 'message', from: 'editor', to: 'judge', subject: 'score' })
    s = applyEvent(s, { id: '2', ts: 60_000, type: 'message', from: 'judge', to: 'editor', subject: 'scores' })
    const edges = liveEdgesFor(s, heraldNodes, 61_000)
    expect(edges.find(e => e.from === 'editor').recent).toBe(false)
    expect(edges.find(e => e.from === 'judge').recent).toBe(true)
    expect(liveEdgesFor(null, heraldNodes, 0)).toEqual([])
  })

  it('re-renders after its container resizes (ResizeObserver, not window resize)', () => {
    const onViewportResize = vi.fn()
    const state = replay(fixture('herald-smoke'), heraldNodes, { org: 'herald' })
    render(<OrgCanvas nodes={heraldNodes} liveState={state} liveNow={state.lastTs} onViewportResize={onViewportResize} />)
    const canvas = screen.getByTestId('org-canvas')
    expect(canvas).toHaveAttribute('data-viewport', '0x0')
    act(() => observers[0].fire(640, 360))
    expect(canvas).toHaveAttribute('data-viewport', '640x360')
    expect(onViewportResize).toHaveBeenCalledWith({ width: 640, height: 360 })
    act(() => observers[0].fire(1280, 720))
    expect(canvas).toHaveAttribute('data-viewport', '1280x720')
    // Cards and edges are still drawn after the resize, no reload needed.
    expect(canvas.querySelectorAll('[data-od-node-id]')).toHaveLength(4)
    expect(screen.getAllByTestId('live-edge').length).toBeGreaterThan(0)
  })
})

describe('OrgDesigner automations and live view', () => {
  const ORG = {
    v: 1, rev: 'r1', valid: true, errors: [],
    org: {
      name: 'growth', goal: 'grow', status: 'active',
      roles: [
        { id: 'lead', title: 'Lead', type: 'boss', reports_to: null, responsibilities: [], ui: { x: 0, y: 0, icon: 'coder' } },
        { id: 'writer', title: 'Writer', type: 'specialist', reports_to: 'lead', responsibilities: [], ui: { x: 0, y: 160, icon: 'coder' } },
        { id: 'publisher-bot', title: 'Publisher', type: 'automation', kind: 'endpoint', reports_to: 'lead', responsibilities: [], automation: { workflow_id: 'wf-pub', alias: 'publish_post' }, endpoint: { url: 'http://x/ep_1' }, ui: { x: 220, y: 160 } },
      ],
    },
  }
  const PUBLISH = { workflow_id: 'wf-pub', alias: 'publish_post', workflow_name: 'Publish post', has_outbound_nodes: true, exists: true }

  beforeEach(() => {
    api.getOrgDesign.mockResolvedValue(ORG)
    api.listOrgAutomations.mockResolvedValue({ v: 1, org: 'growth', automations: [PUBLISH] })
    api.listOrgGrants.mockResolvedValue({ v: 1, org: 'growth', grants: [] })
    api.getOrgAutonomy.mockResolvedValue({ v: 1, level: 'mid', decider: { kind: 'model' }, tiers: {} })
    api.getDaemonStatus.mockResolvedValue({ v: 1, daemon: { running: false }, org_serve: { running: true } })
    api.getOrgLogs.mockResolvedValue({ items: [] })
    api.getOrgReport.mockResolvedValue({ items: [{ run: 'run-old' }] })
    api.addAutomationRole.mockResolvedValue({ v: 1, org: 'growth', role: { id: 'summary-bot', title: 'Summary', reports_to: 'lead' }, endpoint_url: 'http://x' })
    api.saveOrgLayout.mockResolvedValue({ ok: true, rev: 'r2' })
    api.getEffectiveTools.mockResolvedValue({ v: 1, tools: [] })
  })

  async function renderDesigner() {
    const onOpenWorkflow = vi.fn()
    render(<OrgDesigner orgName="growth" onOpenWorkflow={onOpenWorkflow} />)
    await waitFor(() => expect(screen.getByTestId('org-canvas')).toBeInTheDocument())
    return { onOpenWorkflow }
  }

  function dragAutomationFromDrawer(dropOnNodeId) {
    fireEvent.click(screen.getByRole('button', { name: /Automations/ }))
    const row = screen.getByTestId('org-automation')
    fireEvent.mouseDown(row, { button: 0, clientX: 0, clientY: 0 })
    const card = dropOnNodeId ? document.querySelector(`[data-od-node-id="${dropOnNodeId}"]`) : null
    document.elementFromPoint = vi.fn(() => card)
    fireEvent.mouseUp(document, { clientX: 0, clientY: 0 })
  }

  it('warns that the engine is offline on automation roles', async () => {
    await renderDesigner()
    await waitFor(() => expect(screen.getByTestId('engine-offline')).toBeInTheDocument())
    expect(document.querySelector('[data-od-node-id="publisher-bot"] [data-kind="automation"]')).toBeInTheDocument()
  })

  it('dropping an automation on empty canvas adds an automation role under the boss', async () => {
    await renderDesigner()
    await waitFor(() => expect(screen.getByRole('button', { name: /Automations/ })).toBeInTheDocument())
    await screen.findByRole('button', { name: /Automations/ })
    fireEvent.click(screen.getByRole('button', { name: /Automations/ }))
    await screen.findByTestId('org-automation')
    dragAutomationFromDrawer(null)
    await waitFor(() => expect(api.addAutomationRole).toHaveBeenCalledWith('growth', { alias: 'publish_post', reports_to: 'lead', title: 'Publish post' }))
    await waitFor(() => expect(api.saveOrgLayout).toHaveBeenCalledWith('growth', { 'summary-bot': expect.objectContaining({ x: expect.any(Number), y: expect.any(Number) }) }))
  })

  it('dropping an automation on an agent role opens the grant dialog', async () => {
    await renderDesigner()
    fireEvent.click(screen.getByRole('button', { name: /Automations/ }))
    await screen.findByTestId('org-automation')
    dragAutomationFromDrawer('writer')
    const dialog = await screen.findByRole('dialog', { name: 'Grant automation' })
    expect(dialog).toHaveTextContent('Writer may run Publish post')
    expect(within(dialog).getByText('Needs a decision:')).toBeInTheDocument()
    expect(api.addAutomationRole).not.toHaveBeenCalled()
  })

  it('the Live toggle replays the current run through the reducer and makes the canvas read-only', async () => {
    api.getOrgLogs.mockResolvedValue({
      items: [
        { id: 'e1', ts: 10, org: 'growth', run: 'run-1', type: 'status', from: 'writer', reason: 'state-change', data: { from: 'idle', to: 'working' } },
        { id: 'e2', ts: 11, org: 'growth', run: 'run-1', type: 'gate', from: 'lead', data: { gateId: 'g1', name: 'ship' } },
      ],
    })
    await renderDesigner()
    fireEvent.click(screen.getByRole('tab', { name: /Live/ }))
    await waitFor(() => expect(screen.getByTestId('org-canvas')).toHaveAttribute('data-mode', 'live'))
    await waitFor(() => expect(document.querySelector('[data-od-node-id="writer"] [data-live-status="working"]')).toBeInTheDocument())
    expect(document.querySelector('[data-od-node-id="lead"] [data-testid="live-gate"]')).toBeInTheDocument()
    expect(api.getOrgLogs).toHaveBeenCalledWith('growth', '')
    expect(onOrgEvent).toHaveBeenCalled()
    expect(screen.queryByTitle('Delete role')).not.toBeInTheDocument()
    expect(screen.getByTestId('live-summary')).toHaveTextContent('1 working · 1 gate')

    // Picking a past run replays its logs instead.
    fireEvent.change(await screen.findByLabelText('Run to show'), { target: { value: 'run-old' } })
    await waitFor(() => expect(api.getOrgLogs).toHaveBeenCalledWith('growth', 'run-old'))
  })

  it('applies live org:event payloads for this org only', async () => {
    let emit = null
    onOrgEvent.mockImplementation((cb) => { emit = cb; return () => { emit = null } })
    await renderDesigner()
    fireEvent.click(screen.getByRole('tab', { name: /Live/ }))
    await waitFor(() => expect(emit).toBeTypeOf('function'))
    await waitFor(() => expect(api.getOrgLogs).toHaveBeenCalled())
    act(() => {
      emit({ orgName: 'other', event: { id: 'x1', ts: 5, type: 'status', from: 'lead', reason: 'state-change', data: { from: 'idle', to: 'blocked' } } })
      emit({ orgName: 'growth', event: { id: 'x2', ts: 6, type: 'status', from: 'lead', reason: 'state-change', data: { from: 'idle', to: 'working' } } })
    })
    await waitFor(() => expect(document.querySelector('[data-od-node-id="lead"] [data-live-status="working"]')).toBeInTheDocument())
  })

  // #1766: before an org's first run `org logs` says there is no run.
  it('says "No run yet" before the first run, with no spinner and no working count', async () => {
    api.getOrgLogs.mockResolvedValue({ v: 1, org: 'growth', run: null, items: [], no_run: true })
    await renderDesigner()
    fireEvent.click(screen.getByRole('tab', { name: /Live/ }))
    expect(await screen.findByTestId('live-no-run')).toHaveTextContent('No run yet')
    expect(screen.queryByText('Loading…')).not.toBeInTheDocument()
    expect(screen.queryByTestId('live-summary')).not.toBeInTheDocument()
    expect(document.querySelector('.spinner')).not.toBeInTheDocument()
  })

  it('drops "No run yet" as soon as a live event arrives', async () => {
    let emit = null
    onOrgEvent.mockImplementation((cb) => { emit = cb; return () => { emit = null } })
    api.getOrgLogs.mockResolvedValue({ v: 1, org: 'growth', run: null, items: [], no_run: true })
    await renderDesigner()
    fireEvent.click(screen.getByRole('tab', { name: /Live/ }))
    await screen.findByTestId('live-no-run')
    act(() => emit({ orgName: 'growth', event: { id: 'x2', ts: 6, type: 'status', from: 'lead', reason: 'state-change', data: { from: 'idle', to: 'working' } } }))
    await waitFor(() => expect(screen.getByTestId('live-summary')).toHaveTextContent('1 working'))
    expect(screen.queryByTestId('live-no-run')).not.toBeInTheDocument()
  })

  it('does not count working roles while the run is still loading', async () => {
    api.getOrgLogs.mockReturnValue(new Promise(() => {}))
    await renderDesigner()
    fireEvent.click(screen.getByRole('tab', { name: /Live/ }))
    expect(await screen.findByText('Loading…')).toBeInTheDocument()
    expect(screen.queryByTestId('live-summary')).not.toBeInTheDocument()
  })

  it('switches to the grants matrix', async () => {
    await renderDesigner()
    fireEvent.click(screen.getByRole('tab', { name: /Grants/ }))
    expect(await screen.findByRole('grid', { name: 'Grants matrix' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Grant publish_post to writer' })).toBeInTheDocument()
  })
})

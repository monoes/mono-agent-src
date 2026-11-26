// @vitest-environment jsdom
// Screens outside the chat that show an agent turn's result warn when, and only
// when, that turn ran without a sandbox.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup, act } from '@testing-library/react'
import i18n from '../../i18n.js'
import en from '../../locales/en.json'
import { Inspector } from '../../pages/NodeRunner.jsx'
import ApplicationsProcessPendingFlow from '../../pages/ApplicationsProcessPendingFlow.jsx'
import Info from '../../pages/Info.jsx'
import RecordingReview from '../../pages/connections/RecordingReview.jsx'
import CaptureViewerModal from '../CaptureViewerModal.jsx'
import { StageDrawer } from '../stage/StageDrawer.jsx'
import { StageNode } from '../stage/StageNode.jsx'
import { replayStage } from '../../lib/orgStage.js'
import * as WailsApp from '../../wailsjs/go/main/App'
import { api } from '../../services/api.js'

vi.mock('../../wailsjs/go/main/App', async (importOriginal) => ({
  ...await importOriginal(),
  GetVaultImages: vi.fn(() => Promise.resolve([])),
  EvaluateApplication: vi.fn(), HasGeneratedDocuments: vi.fn(() => Promise.resolve(true)),
  GetCaptureView: vi.fn(), GetProfileInfoFields: vi.fn(), GetProfileInfo: vi.fn(),
  GetProfileInfoPhoto: vi.fn(() => Promise.resolve('')), AIFillProfileInfo: vi.fn(),
}))
vi.mock('../../services/api.js', () => ({
  api: { analyzeRecording: vi.fn(), openURL: vi.fn() }, notify: vi.fn(), reportRunFailure: vi.fn(), subscribeEvent: vi.fn(() => () => {}),
}))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: vi.fn(() => Promise.resolve(true)) }))

const NO_SANDBOX = /No sandbox for this turn: the agent is not confined to a working folder\./
const NO_SANDBOX_ES = /Este turno no tiene sandbox/

afterEach(async () => {
  cleanup()
  await act(() => i18n.changeLanguage('en'))
})

const warned = async (text = NO_SANDBOX) => expect((await screen.findByText(text, undefined, { timeout: 5000 })).closest('[role="status"]')).toBeTruthy()
const silent = () => expect(screen.queryByText(NO_SANDBOX)).toBeNull()

describe('workflow editor node output', () => {
  const node = (subtype, item, config = {}) => ({
    id: 'n1', subtype, label: 'Node', category: 'ai', config, schema: { fields: [] },
    runStatus: 'ok', runOutputs: [{ handle: 'main', items: [item] }],
  })
  const draw = n => render(<Inspector node={n} onConfigChange={vi.fn()} onClose={vi.fn()} liveSchemas={{}} automationIds={new Set()} />)

  for (const [name, n] of [
    ['a test run item', node('agent.ask', { _agent_sandbox: 'unsupported' })],
    ['a stored execution item', node('agent.ask', { json: { _agent_sandbox: 'off' } })],
    ['guardrails', node('ai.guardrails', { _agent_sandbox: 'needs-monomind' })],
    ['evaluation metrics', node('core.evaluation', { _agent_sandbox: 'awaiting-monomind' }, { operation: 'setMetrics' })],
    ['page extraction', node('ai.extract_page', { _agent_sandbox: 'off' })],
    ['fit evaluation', node('applications.evaluate', { _agent_sandbox: 'off' })],
    ['browser.jev', node('browser.jev', { _text_agent_sandbox: 'unsupported' })],
  ]) {
    it(`warns on ${name}`, async () => { draw(n); await warned() })
  }

  for (const [name, n] of [
    ['a sandboxed turn', node('agent.ask', { _agent_sandbox: 'sandboxed' })],
    ['a scoped turn', node('agent.ask', { _agent_sandbox: 'scoped' })],
    ['a restricted turn', node('agent.ask', { _agent_sandbox: 'restricted' })],
    ['a node that only passes another turn\'s item on', node('core.set', { _agent_sandbox: 'off' })],
    ['an evaluation operation that passes items on', node('core.evaluation', { _agent_sandbox: 'off' }, { operation: 'setOutputs' })],
    ['browser.jev with an incoming item\'s verdict', node('browser.jev', { _agent_sandbox: 'off' })],
  ]) {
    it(`is silent for ${name}`, () => { draw(n); silent() })
  }

  it('is said in Spanish', async () => {
    await act(() => i18n.changeLanguage('es'))
    draw(node('agent.ask', { _agent_sandbox: 'off' }))
    await warned(NO_SANDBOX_ES)
  })
})

describe('application fit verdict', () => {
  const start = async extra => {
    WailsApp.EvaluateApplication.mockResolvedValue({ verdict: 'GOOD', overall_score: 8, rationale: 'solid fit', ...extra })
    render(<ApplicationsProcessPendingFlow pendingApplications={[{ id: 'a1', title: 'Engineer', company: 'Acme' }]} onClose={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Start' }))
    await screen.findByText('solid fit')
  }
  it('warns for an unsandboxed turn', async () => { await start({ sandbox_status: 'unsupported' }); await warned() })
  it('is silent for a sandboxed one', async () => { await start({}); silent() })
})

describe('capture summary', () => {
  const open = async sandbox => {
    WailsApp.GetCaptureView.mockResolvedValue({ summary: 'the summary text', summary_status: { status: 'done', runtime: 'codex', sandbox } })
    render(<CaptureViewerModal doc={{ id: 'd1', filename: 'page' }} onClose={() => {}} />)
    await screen.findByText('the summary text')
  }
  it('warns for an unsandboxed turn', async () => { await open('off'); await warned() })
  it('is silent for a sandboxed one', async () => { await open(undefined); silent() })
})

describe('recording analysis', () => {
  const open = async extra => {
    api.analyzeRecording.mockResolvedValue({ draftDir: '/d/r', draft: { action: 'create_contact', actionDef: { description: 'Create a contact', steps: [] }, lint: [], inputs: [] }, ...extra })
    render(<RecordingReview recording={{ id: 'rec1', title: 'Rec' }} automationId="acme" onBack={() => {}} />)
    await screen.findByText('Create a contact', undefined, { timeout: 5000 })
  }
  it('warns for an unsandboxed turn', async () => { await open({ sandboxStatus: 'unsupported' }); await warned() })
  it('is silent for a sandboxed one', async () => { await open({}); silent() })
})

describe('profile AI fill', () => {
  const fill = async extra => {
    WailsApp.GetProfileInfoFields.mockResolvedValue({ groups: [{ id: 'identity', label: 'Identity' }], fields: [{ key: 'identity.first_name', label: 'First name', group: 'identity', kind: 'text' }] })
    WailsApp.GetProfileInfo.mockResolvedValue({ profile: 'p', entries: [] })
    WailsApp.AIFillProfileInfo.mockResolvedValue({ asked: 1, filled: [], skipped: 1, ...extra })
    render(<Info />)
    await screen.findByLabelText('First name', undefined, { timeout: 5000 })
    fireEvent.click(screen.getByRole('button', { name: en.info.aiFill }))
    await screen.findByRole('button', { name: en.info.aiFill }, { timeout: 5000 })
    await act(() => new Promise(r => setTimeout(r, 20)))
  }
  it('warns for an unsandboxed turn', async () => { await fill({ sandbox_status: 'off' }); await warned() })
  it('is silent for a sandboxed one', async () => { await fill({}); silent() })
})

describe('org stage research worker', () => {
  const events = confinement => [
    { type: 'agent.spawned', seq: 1, payload: { agentId: 'w1', role: 'scout', agentType: 'worker', runtime: 'copilot' } },
    { type: 'agent.status', seq: 2, payload: { agentId: 'w1', from: 'starting', to: 'working', confinement } },
    { type: 'agent.status', seq: 3, payload: { agentId: 'w1', from: 'working', to: 'done' } },
  ]
  const worker = confinement => replayStage(events(confinement)).nodes.w1
  const WORKER = /No sandbox for this agent/

  it('warns in the drawer and marks the card, and keeps both once done', () => {
    const node = worker('write-lease')
    render(<StageDrawer node={node} onClose={() => {}} />)
    expect(screen.getByText(WORKER).closest('[role="status"]')).toBeTruthy()
    cleanup()
    render(<StageNode node={node} x={0} y={0} onSelect={() => {}} />)
    expect(screen.getByTestId('stage-unsandboxed').textContent).toBe('No sandbox')
    expect(screen.getByRole('button').getAttribute('aria-label')).toMatch(/No sandbox: this agent has full access/)
  })

  it('is silent for a confined worker', () => {
    for (const confinement of ['access-read', 'sandbox-read-only', undefined]) {
      const node = worker(confinement)
      render(<StageDrawer node={node} onClose={() => {}} />)
      expect(screen.queryByText(WORKER)).toBeNull()
      cleanup()
      render(<StageNode node={node} x={0} y={0} onSelect={() => {}} />)
      expect(screen.queryByTestId('stage-unsandboxed')).toBeNull()
      cleanup()
    }
  })

  it('is said in Spanish on the card', async () => {
    await act(() => i18n.changeLanguage('es'))
    render(<StageNode node={worker('write-lease')} x={0} y={0} onSelect={() => {}} />)
    expect(screen.getByTestId('stage-unsandboxed').textContent).toBe('Sin sandbox')
  })
})

// @vitest-environment jsdom
// Your first run: a starter runs for real (template -> workflow -> switched on ->
// RunWorkflow), its nodes light from the execution detail the events point at,
// and only a successful run marks the step.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, act, waitFor } from '@testing-library/react'

const go = vi.hoisted(() => ({
  CreateWorkflowFromTemplate: vi.fn(),
  GetWorkflow: vi.fn(),
  SetWorkflowActive: vi.fn(),
  RunWorkflow: vi.fn(),
  GetExecutionDetail: vi.fn(),
}))
vi.mock('../../wailsjs/go/main/App', () => go)
const events = vi.hoisted(() => ({ handlers: {}, offs: [] }))
vi.mock('../../services/api.js', () => ({
  subscribeEvent: vi.fn((name, fn) => {
    events.handlers[name] = fn
    const off = vi.fn(() => { if (events.handlers[name] === fn) delete events.handlers[name] })
    events.offs.push(off)
    return off
  }),
}))

import i18n from '../../i18n.js'
import FirstRun from './FirstRun.jsx'

const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const stateOf = (done = [], skipped = []) => ({ completed: false, currentStep: 'firstRun', profileId: 'p1', steps: IDS.map(id => ({ id, status: done.includes(id) ? 'done' : skipped.includes(id) ? 'skipped' : 'todo' })) })

const WF_NODES = [
  { id: 'n3', name: 'Pick Title and First Paragraph', node_type: 'data.html', position_x: 640 },
  { id: 'n1', name: 'Start', node_type: 'trigger.manual', position_x: 0 },
  { id: 'n2', name: 'Fetch Page', node_type: 'http.request', position_x: 320 },
]
const detail = (status, nodes, error = '') => ({ id: 'ex1', workflow_id: 'wf1', status, error, nodes })

let props
function renderStep(done = [], skipped = []) {
  props = {
    state: stateOf(done, skipped), profileId: 'p1', mode: 'full',
    reload: vi.fn(), skip: vi.fn(), complete: vi.fn(), onDone: vi.fn(), onFinish: vi.fn(),
    mark: vi.fn(() => Promise.resolve({ ok: true })),
    goTo: vi.fn(), setCanContinue: vi.fn(), setOnContinue: vi.fn(), setContinueLabel: vi.fn(),
  }
  return render(<div className="onb-root"><FirstRun {...props} /></div>)
}
const lastCanContinue = () => props.setCanContinue.mock.calls.at(-1)?.[0]
const emit = async (name, data) => { await act(async () => { events.handlers[name]?.(data) }) }

beforeEach(async () => {
  await i18n.changeLanguage('en')
  events.handlers = {}
  events.offs = []
  go.CreateWorkflowFromTemplate.mockReset().mockResolvedValue({ id: 'wf1', name: 'Starter' })
  go.GetWorkflow.mockReset().mockResolvedValue({ id: 'wf1', nodes: WF_NODES, connections: [] })
  go.SetWorkflowActive.mockReset().mockResolvedValue(undefined)
  go.RunWorkflow.mockReset().mockResolvedValue(undefined)
  go.GetExecutionDetail.mockReset()
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

async function startPageRead() {
  fireEvent.click(screen.getByRole('button', { name: /Read a web page/ }))
  await waitFor(() => expect(go.RunWorkflow).toHaveBeenCalledWith('wf1'))
}

describe('Your first run', () => {
  it('disables a card whose prerequisite step is not done and names the step', () => {
    renderStep(['agents'])
    expect(screen.getByRole('button', { name: /Read a web page/ })).toBeEnabled()
    expect(screen.getByRole('button', { name: /Summarise a web page/ })).toBeEnabled()
    // the browser starter needs agents, browser and permissions: browser is the first missing
    expect(screen.queryByRole('button', { name: /Read a page in your browser/ })).not.toBeInTheDocument()
    expect(screen.getByText("Finish 'Connect your browser' first.")).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Go to Connect your browser' }))
    expect(props.goTo).toHaveBeenCalledWith('browser')
    expect(lastCanContinue()).toBe(false)
  })

  it('says each run adds a workflow, and creates or switches on nothing before a click', async () => {
    renderStep(['agents', 'browser', 'permissions'])
    expect(screen.getAllByText('Running it adds this workflow to your list.')).toHaveLength(3)
    await act(async () => {})
    expect(go.CreateWorkflowFromTemplate).not.toHaveBeenCalled()
    expect(go.SetWorkflowActive).not.toHaveBeenCalled()
    expect(go.RunWorkflow).not.toHaveBeenCalled()
  })

  it('counts a skipped step as not done', () => {
    renderStep(['browser', 'permissions'], ['agents'])
    expect(screen.queryByRole('button', { name: /Summarise a web page/ })).not.toBeInTheDocument()
    expect(screen.getAllByText("Finish 'Your agents' first.")).toHaveLength(2)
  })

  it('runs the chosen template for real', async () => {
    renderStep([])
    await startPageRead()
    expect(go.CreateWorkflowFromTemplate).toHaveBeenCalledWith('starter_page_read')
    expect(go.GetWorkflow).toHaveBeenCalledWith('wf1')
    expect(go.SetWorkflowActive).toHaveBeenCalledWith('wf1', true)
    const order = [go.CreateWorkflowFromTemplate, go.SetWorkflowActive, go.RunWorkflow].map(f => f.mock.invocationCallOrder[0])
    expect(order).toEqual([...order].sort((a, b) => a - b))
    // listening before the run starts
    expect(events.handlers['workflow:exec-started']).toBeTypeOf('function')
    expect(events.handlers['workflow:complete']).toBeTypeOf('function')
    // the node strip is the workflow's own nodes, in run order
    expect(screen.getAllByRole('listitem').map(li => li.textContent)).toEqual(['Start', 'Fetch Page', 'Pick Title and First Paragraph'])
    // every card waits while it runs
    expect(screen.getByRole('button', { name: /Read a web page/ })).toBeDisabled()
  })

  it('lights nodes from execution events and shows the result', async () => {
    renderStep([])
    await startPageRead()
    go.GetExecutionDetail.mockResolvedValueOnce(detail('RUNNING', [
      { node_id: 'n1', node_name: 'Start', status: 'SUCCESS' },
      { node_id: 'n2', node_name: 'Fetch Page', status: 'RUNNING' },
    ]))
    await emit('workflow:exec-started', { workflow_id: 'wf1', execution_id: 'ex1' })
    await waitFor(() => expect(screen.getByText('Running: Fetch Page…')).toBeInTheDocument())
    expect(go.GetExecutionDetail).toHaveBeenCalledWith('ex1')
    expect(screen.getByRole('listitem', { name: 'Start: done' })).toBeInTheDocument()
    expect(screen.getByRole('listitem', { name: 'Fetch Page: running' })).toBeInTheDocument()
    expect(props.mark).not.toHaveBeenCalled()

    go.GetExecutionDetail.mockResolvedValue(detail('SUCCESS', [
      { node_id: 'n1', node_name: 'Start', status: 'SUCCESS' },
      { node_id: 'n2', node_name: 'Fetch Page', status: 'SUCCESS' },
      { node_id: 'n3', node_name: 'Pick Title and First Paragraph', status: 'SUCCESS', output_items: JSON.stringify([{ json: { title: 'Example Domain', first_paragraph: 'This domain is for use in documentation examples.' } }]) },
    ]))
    await emit('workflow:complete', { workflow_id: 'wf1', success: true })
    await waitFor(() => expect(screen.getByText('Example Domain')).toBeInTheDocument())
    expect(screen.getByText('This domain is for use in documentation examples.')).toBeInTheDocument()
    expect(screen.getByRole('listitem', { name: 'Pick Title and First Paragraph: done' })).toBeInTheDocument()
    expect(props.mark).toHaveBeenCalledWith('firstRun')
    expect(lastCanContinue()).toBe(true)
    // the run is over: the listeners are gone
    expect(events.offs.every(off => off.mock.calls.length > 0)).toBe(true)
  })

  it('a failed run shows the error, allows Continue and does not mark the step', async () => {
    renderStep([])
    await startPageRead()
    go.GetExecutionDetail.mockResolvedValue(detail('FAILED', [
      { node_id: 'n1', node_name: 'Start', status: 'SUCCESS' },
      { node_id: 'n2', node_name: 'Fetch Page', status: 'FAILED', error_message: 'dial tcp: lookup example.com: no such host' },
    ], 'node "Fetch Page" failed'))
    await emit('workflow:exec-started', { workflow_id: 'wf1', execution_id: 'ex1' })
    await emit('workflow:complete', { workflow_id: 'wf1', success: false })
    await waitFor(() => expect(screen.getByText('dial tcp: lookup example.com: no such host')).toBeInTheDocument())
    expect(screen.getByRole('listitem', { name: 'Fetch Page: failed' })).toBeInTheDocument()
    expect(props.mark).not.toHaveBeenCalled()
    expect(lastCanContinue()).toBe(true)

    // Try again runs the same workflow: no second copy
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(go.RunWorkflow).toHaveBeenCalledTimes(2))
    expect(go.CreateWorkflowFromTemplate).toHaveBeenCalledTimes(1)
  })

  it('asks for the site approval when the browser waits for it', async () => {
    renderStep(['agents', 'browser', 'permissions'])
    go.GetWorkflow.mockResolvedValue({ id: 'wf1', nodes: [{ id: 'b1', name: 'Read in Browser', position_x: 320 }] })
    fireEvent.click(screen.getByRole('button', { name: /Read a page in your browser/ }))
    await waitFor(() => expect(go.RunWorkflow).toHaveBeenCalledWith('wf1'))
    expect(go.CreateWorkflowFromTemplate).toHaveBeenCalledWith('starter_browser_read')
    go.GetExecutionDetail.mockResolvedValue(detail('FAILED', [
      { node_id: 'b1', node_name: 'Read in Browser', status: 'FAILED', error_message: "https://example.com is a new site for chat and waits for the user's approval (HIL item h1): ask the user to allow it" },
    ]))
    await emit('workflow:exec-started', { workflow_id: 'wf1', execution_id: 'ex1' })
    await emit('workflow:complete', { workflow_id: 'wf1', success: false })
    await waitFor(() => expect(screen.getByText(/Approve it in Human-in-Loop, then run it again/)).toBeInTheDocument())
    expect(props.mark).not.toHaveBeenCalled()
  })

  it('a run that stops before it starts says so', async () => {
    renderStep([])
    await startPageRead()
    await emit('workflow:complete', { workflow_id: 'wf2', success: false }) // another workflow: ignored
    expect(screen.queryByText('The run stopped before it started.')).not.toBeInTheDocument()
    await emit('workflow:complete', { workflow_id: 'wf1', success: false })
    expect(screen.getByText('The run stopped before it started.')).toBeInTheDocument()
    expect(go.GetExecutionDetail).not.toHaveBeenCalled()
    expect(lastCanContinue()).toBe(true)
  })

  it('a sign-in refusal shows the message and gives the cards back', async () => {
    go.CreateWorkflowFromTemplate.mockRejectedValue('Log in to monoes.me first: your session ended.')
    renderStep([])
    fireEvent.click(screen.getByRole('button', { name: /Read a web page/ }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(i18n.t('onboarding.shell.signIn')))
    expect(screen.getByRole('alert')).toHaveClass('onb-error')
    expect(go.RunWorkflow).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: /Read a web page/ })).toBeEnabled()
  })

  it('stops listening when the step is left mid-run', async () => {
    const { unmount } = renderStep([])
    await startPageRead()
    unmount()
    expect(events.offs.length).toBe(2)
    expect(events.offs.every(off => off.mock.calls.length > 0)).toBe(true)
  })
})

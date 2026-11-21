// @vitest-environment jsdom
// A tool card whose result names a real entity (create_workflow, create_org,
// save_document) offers "Open in <tab>", checked against the backend first.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import '../../i18n.js'

vi.mock('../../services/hilApi.js', () => ({ listAsks: vi.fn(async () => []), resolve: vi.fn() }))
vi.mock('../../services/api.js', () => ({
  api: { getWorkflow: vi.fn(), listOrgDesigns: vi.fn(), getProfileDocument: vi.fn(), getVaultImage: vi.fn() },
  notify: vi.fn(),
}))
import { api, notify } from '../../services/api.js'
import { ChatTimeline } from './ChatTimeline.jsx'
import { chatReducer, initialChatState } from './chatReducer.js'
import { artifactTarget } from './chatArtifacts.js'

afterEach(() => { cleanup(); vi.clearAllMocks() })

let seq = 0
const ev = (type, payload) => ({ type: 'event', event: { conversationId: 'c1', turnId: 't1', seq: ++seq, at: '2026-10-19T10:00:00.000Z', type, payload } })
const state = (name, result, ok = true) => [
  ev('tool.started', { callId: 'c1', name, arguments: {} }),
  ev('tool.completed', { callId: 'c1', ok, result: JSON.stringify(result) }),
].reduce(chatReducer, initialChatState())

describe('artifactTarget', () => {
  it('maps a resolved artifact to navigate(page, data)', () => {
    expect(artifactTarget({ type: 'workflow', id: 'w1' })).toEqual({ page: 'noderunner', data: { workflowId: 'w1' } })
    expect(artifactTarget({ type: 'org', name: 'Acme' })).toEqual({ page: 'orgs', data: { org: 'Acme' } })
    expect(artifactTarget({ type: 'document', id: 'd1' })).toEqual({ page: 'documents', data: null })
    expect(artifactTarget({ type: 'image', id: 'i1' })).toBeNull()
    expect(artifactTarget(null)).toBeNull()
  })
})

describe('tool entity link', () => {
  const mount = (st, onNavigate = vi.fn(), extra = {}) =>
    render(<ChatTimeline state={st} turnId="t1" isLive={false} onNavigate={onNavigate} {...extra} />)

  it('opens a created workflow in its tab, with its data', async () => {
    api.getWorkflow.mockResolvedValue({ id: 'w1', name: 'Digest' })
    const nav = vi.fn()
    mount(state('create_workflow', { workflow_id: 'w1' }), nav)
    fireEvent.click(await screen.findByRole('button', { name: 'Open in Workflows' }))
    await waitFor(() => expect(nav).toHaveBeenCalledWith('noderunner', { workflowId: 'w1' }))
  })

  it('opens a created org by name', async () => {
    api.listOrgDesigns.mockResolvedValue([{ name: 'Acme' }])
    const nav = vi.fn()
    mount(state('create_org', { org_name: 'Acme' }), nav)
    fireEvent.click(await screen.findByRole('button', { name: 'Open in Orgs' }))
    await waitFor(() => expect(nav).toHaveBeenCalledWith('orgs', { org: 'Acme' }))
  })

  it('offers nothing for a forged id, a failed call or an unrelated tool', async () => {
    api.getWorkflow.mockResolvedValue(null)
    mount(state('create_workflow', { workflow_id: 'ghost' }))
    await waitFor(() => expect(api.getWorkflow).toHaveBeenCalled())
    cleanup()
    mount(state('create_workflow', { workflow_id: 'w1' }, false))
    mount(state('list_things', { workflow_id: 'w1' }))
    await new Promise(r => setTimeout(r, 20))
    expect(screen.queryByTestId('tool-link')).toBeNull()
    expect(api.getWorkflow).toHaveBeenCalledTimes(1)
  })

  it('revalidates on click and does not navigate when the target is gone', async () => {
    api.getWorkflow.mockResolvedValueOnce({ id: 'w1' }).mockResolvedValue(null)
    const nav = vi.fn()
    mount(state('create_workflow', { workflow_id: 'w1' }), nav)
    fireEvent.click(await screen.findByRole('button', { name: 'Open in Workflows' }))
    await waitFor(() => expect(notify).toHaveBeenCalled())
    expect(nav).not.toHaveBeenCalled()
  })

  it('is off when entityLinks is false, and without onNavigate', async () => {
    api.getWorkflow.mockResolvedValue({ id: 'w1' })
    mount(state('create_workflow', { workflow_id: 'w1' }), vi.fn(), { entityLinks: false })
    cleanup()
    render(<ChatTimeline state={state('create_workflow', { workflow_id: 'w1' })} turnId="t1" isLive={false} />)
    await new Promise(r => setTimeout(r, 20))
    expect(screen.queryByTestId('tool-link')).toBeNull()
    expect(api.getWorkflow).not.toHaveBeenCalled()
  })
})

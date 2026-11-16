import { describe, it, expect, vi } from 'vitest'
import { toggleActive, loadWorkflowChecked, openRunWorkflow, shouldConfirmRunSwitch } from './editorFailures.js'

describe('toggleActive', () => {
  it('keeps the new state when the save succeeds', async () => {
    const setActive = vi.fn(), notify = vi.fn()
    await toggleActive({ id: 'w', prev: false, next: true, setActive, save: async () => {}, notify })
    expect(setActive.mock.calls).toEqual([[true]])
    expect(notify).not.toHaveBeenCalled()
  })
  it('reverts the state and says why when the save fails', async () => {
    const setActive = vi.fn(), notify = vi.fn()
    await toggleActive({ id: 'w', prev: false, next: true, setActive, save: async () => { throw new Error('db locked') }, notify })
    expect(setActive.mock.calls).toEqual([[true], [false]])
    expect(notify).toHaveBeenCalledWith('activate workflow', expect.stringContaining('db locked'))
  })
  it('does not call the backend for an unsaved workflow', async () => {
    const save = vi.fn(), setActive = vi.fn()
    await toggleActive({ id: null, prev: false, next: true, setActive, save, notify: vi.fn() })
    expect(save).not.toHaveBeenCalled()
    expect(setActive.mock.calls).toEqual([[true]])
  })
})

describe('loadWorkflowChecked', () => {
  it('stops polling only after the workflow loaded', async () => {
    const order = []
    const wf = { id: 'w' }
    const got = await loadWorkflowChecked({ id: 'w', getWorkflow: async () => { order.push('get'); return wf }, stopPolling: () => order.push('stop'), notify: vi.fn() })
    expect(got).toBe(wf)
    expect(order).toEqual(['get', 'stop'])
  })
  it('keeps polling and notifies when the load throws', async () => {
    const stopPolling = vi.fn(), notify = vi.fn()
    const got = await loadWorkflowChecked({ id: 'w', getWorkflow: async () => { throw new Error('gone') }, stopPolling, notify })
    expect(got).toBeNull()
    expect(stopPolling).not.toHaveBeenCalled()
    expect(notify).toHaveBeenCalledWith('load workflow', expect.stringContaining('gone'))
  })
  it('keeps polling and notifies when the workflow is missing', async () => {
    const stopPolling = vi.fn(), notify = vi.fn()
    expect(await loadWorkflowChecked({ id: 'w', getWorkflow: async () => null, stopPolling, notify })).toBeNull()
    expect(stopPolling).not.toHaveBeenCalled()
    expect(notify).toHaveBeenCalledWith('load workflow', expect.any(String))
  })
})

describe('shouldConfirmRunSwitch', () => {
  it('asks only when a dirty canvas would be replaced by another workflow', () => {
    expect(shouldConfirmRunSwitch({ isDirty: true, currentId: 'a', targetId: 'b' })).toBe(true)
    expect(shouldConfirmRunSwitch({ isDirty: true, currentId: 'a', targetId: 'a' })).toBe(false)
    expect(shouldConfirmRunSwitch({ isDirty: false, currentId: 'a', targetId: 'b' })).toBe(false)
  })
})

describe('openRunWorkflow', () => {
  const detail = { id: 'x1', workflow_id: 'b' }
  it('cancels without loading when the user keeps the dirty canvas', async () => {
    const getWorkflow = vi.fn()
    const r = await openRunWorkflow({ detail, wfId: 'a', needLoad: true, isDirty: true, getWorkflow, confirmDiscard: async () => false, notify: vi.fn() })
    expect(r).toEqual({ cancelled: true })
    expect(getWorkflow).not.toHaveBeenCalled()
  })
  it('returns the workflow when confirmed', async () => {
    const wf = { id: 'b' }
    const r = await openRunWorkflow({ detail, wfId: 'a', needLoad: true, isDirty: true, getWorkflow: async () => wf, confirmDiscard: async () => true, notify: vi.fn() })
    expect(r).toEqual({ wf })
  })
  it('notifies and returns no workflow when it was deleted', async () => {
    const notify = vi.fn()
    const r = await openRunWorkflow({ detail, wfId: 'a', needLoad: true, isDirty: false, getWorkflow: async () => null, confirmDiscard: vi.fn(), notify })
    expect(r).toEqual({ wf: null })
    expect(notify).toHaveBeenCalledWith('open run', expect.stringContaining('b'))
  })
  it('notifies when the load throws', async () => {
    const notify = vi.fn()
    const r = await openRunWorkflow({ detail, wfId: 'a', needLoad: true, isDirty: false, getWorkflow: async () => { throw new Error('boom') }, confirmDiscard: vi.fn(), notify })
    expect(r).toEqual({ wf: null })
    expect(notify).toHaveBeenCalledWith('open run', expect.stringContaining('boom'))
  })
  it('does not load when the canvas already holds the workflow', async () => {
    const getWorkflow = vi.fn()
    const r = await openRunWorkflow({ detail, wfId: 'b', needLoad: false, isDirty: true, getWorkflow, confirmDiscard: vi.fn(), notify: vi.fn() })
    expect(r).toEqual({ wf: null })
    expect(getWorkflow).not.toHaveBeenCalled()
  })
})

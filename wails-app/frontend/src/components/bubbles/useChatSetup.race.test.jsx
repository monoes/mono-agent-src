// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'

// The write-back effect of useChatSetup runs after the commit that decided it. A
// level or runtime the user picks between that commit and the effect must not be put back:
// the write-back is a patch that applies only while the draft still holds what the effect saw.
const { api } = vi.hoisted(() => ({
  api: { coderStatus: vi.fn(), getAgentRuntimeModels: vi.fn() },
}))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn() }))
vi.mock('../../lib/agentRuntimes.js', async importOriginal => ({ ...(await importOriginal()), cachedAgentScan: vi.fn().mockResolvedValue({ agents: [] }) }))

import { useChatSetup } from './useChatSetup.js'

const coderOn = { enabled: true, ready: true, defaultAccess: 'full', runtimes: [{ id: 'claude', ready: true, effort: true }, { id: 'gemini', ready: true, effort: true }] }

// apply runs one patch the way the overlay does: an object merges, a function is given the state.
const apply = (state, patch) => ({ ...state, ...(typeof patch === 'function' ? patch(state) : patch) })

beforeEach(() => {
  vi.clearAllMocks()
  api.coderStatus.mockResolvedValue(coderOn)
  api.getAgentRuntimeModels.mockResolvedValue([{ id: 'opus', label: 'Opus' }])
})

describe('useChatSetup write-back', () => {
  it('does not put the resolved default back over a level the user picked in between', async () => {
    const patches = []
    const { result } = renderHook(() => useChatSetup(true, { workspace: { kind: 'root' }, runtime: '', model: '', effort: '' }, p => patches.push(p)))
    await waitFor(() => expect(result.current.settled).toBe(true))
    expect(result.current.access).toBe('full')
    // The effect asked for the default level; by the time it applies, the user has picked coding.
    const accessPatches = patches.filter(p => typeof p === 'function' || 'access' in p)
    expect(accessPatches.length).toBeGreaterThan(0)
    let state = { workspace: { kind: 'root' }, runtime: '', model: '', effort: '', access: 'coding' }
    for (const p of accessPatches) state = apply(state, p)
    expect(state.access).toBe('coding')
    // and while nothing was picked, the default is still written
    let fresh = { workspace: { kind: 'root' }, runtime: '', model: '', effort: '' }
    for (const p of accessPatches) fresh = apply(fresh, p)
    expect(fresh.access).toBe('full')
  })

  it('does not put the first runtime back over a runtime the user picked in between', async () => {
    const patches = []
    renderHook(() => useChatSetup(true, { workspace: { kind: 'root' }, access: 'coding', runtime: '', model: '', effort: '' }, p => patches.push(p)))
    await waitFor(() => expect(patches.some(p => typeof p === 'function')).toBe(true))
    let state = { access: 'coding', runtime: 'gemini', model: '', effort: '' }
    for (const p of patches) state = apply(state, p)
    expect(state.runtime).toBe('gemini')
    let fresh = { access: 'coding', runtime: '', model: '', effort: '' }
    for (const p of patches) fresh = apply(fresh, p)
    expect(fresh.runtime).toBe('claude')
  })
})

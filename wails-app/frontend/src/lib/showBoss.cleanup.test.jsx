// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { showBossInAgents, cancelShowBoss } from './showBoss.js'

// The poll's timers must not outlive the wait: a cancel clears them, and a poll that fires after the page environment is gone
// stops without touching `document` (an unhandled ReferenceError there fails the whole vitest run).
beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { cancelShowBoss(); vi.unstubAllGlobals(); vi.useRealTimers() })

describe('showBossInAgents timers', () => {
  it('cancelShowBoss clears the pending poll and the listener-arming timers', async () => {
    const p = showBossInAgents(vi.fn(), { intervalMs: 100, timeoutMs: 4000 })
    expect(vi.getTimerCount()).toBeGreaterThan(0)
    cancelShowBoss()
    expect(vi.getTimerCount()).toBe(0)
    expect(await p).toBe(false)
  })
  it('a poll that fires after the document is gone resolves false, throws nothing and leaves no timer', async () => {
    const p = showBossInAgents(vi.fn(), { intervalMs: 100, timeoutMs: 4000 })
    vi.stubGlobal('document', undefined)
    await expect(vi.advanceTimersByTimeAsync(5000)).resolves.not.toThrow()
    expect(await p).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
  })
})

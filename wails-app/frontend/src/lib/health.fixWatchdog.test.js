// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'

const handlers = []
const off = vi.fn()
vi.mock('../wailsjs/go/main/App', () => ({
  RunHealthCheck: vi.fn(),
  CancelHealthRun: vi.fn(),
  RunHealthFix: vi.fn(() => Promise.resolve('{}')),
  InstallAgentRuntime: vi.fn(),
}))
vi.mock('../services/api.js', () => ({
  subscribeEvent: (_name, fn) => { handlers.push(fn); return off },
}))

import { runFix, FIX_IDLE_TIMEOUT_MS } from './health.js'

afterEach(() => { vi.useRealTimers(); handlers.length = 0 })

describe('runFix watchdog', () => {
  it('settles and releases the lock when no event ever arrives', async () => {
    vi.useFakeTimers()
    const p = runFix('doctor.fix')
    await vi.advanceTimersByTimeAsync(FIX_IDLE_TIMEOUT_MS + 10)
    const r = await p
    expect(r.ok).toBe(false)
    expect(r.message).toMatch(/no progress/i)
    // The lock is free again: a new run starts instead of reporting busy.
    const again = runFix('doctor.fix')
    await vi.advanceTimersByTimeAsync(0)
    expect(handlers.length).toBe(2)
    await vi.advanceTimersByTimeAsync(FIX_IDLE_TIMEOUT_MS + 10)
    expect((await again).busy).toBeUndefined()
  })

  it('a progress line keeps the run alive, and the done event still settles it', async () => {
    vi.useFakeTimers()
    const p = runFix('doctor.fix2')
    await vi.advanceTimersByTimeAsync(FIX_IDLE_TIMEOUT_MS - 1000)
    handlers[0]({ fix_id: 'doctor.fix2', kind: 'line', message: 'working' })
    await vi.advanceTimersByTimeAsync(FIX_IDLE_TIMEOUT_MS - 1000)
    handlers[0]({ fix_id: 'doctor.fix2', kind: 'done', message: 'ok' })
    expect(await p).toEqual({ ok: true, message: 'ok' })
  })
})

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockRunHealthCheck = vi.fn()
const mockCancel = vi.fn()
vi.mock('../wailsjs/go/main/App', () => ({
  RunHealthCheck: (...a) => mockRunHealthCheck(...a),
  CancelHealthRun: (...a) => mockCancel(...a),
  RunHealthFix: vi.fn(),
  InstallAgentRuntime: vi.fn(),
}))
vi.mock('../services/api.js', () => ({ subscribeEvent: () => () => {} }))

import { groupReport, summarize, fixPlan, versionSkew, mergeReports, healthMode, runKey } from './health.js'

const report = {
  v: 1,
  results: [
    { id: 'core.db', group: 'core', title: 'Database', status: 'fail', required: true,
      fix: { id: 'core.db.migrate', label: 'Migrate', safety: 'auto' } },
    { id: 'core.disk', group: 'core', title: 'Disk', status: 'ok' },
    { id: 'monomind.node', group: 'monomind', title: 'Node.js', status: 'fail',
      fix: { id: 'monomind.node.install', label: 'Install Node', safety: 'confirm' } },
    { id: 'runtimes.agents', group: 'runtimes', title: 'Runtimes', status: 'ok' },
    { id: 'runtimes.codex', group: 'runtimes', parent: 'runtimes.agents', title: 'codex', status: 'info',
      fix: { id: 'runtimes.install:codex', label: 'Install codex', safety: 'confirm', optional: true } },
    { id: 'browser.extension', group: 'browser', title: 'Extension', status: 'warn',
      fix: { id: 'browser.extension.install', label: 'Load it', safety: 'manual', command: 'chrome://extensions' } },
    { id: 'monomind.doctor', group: 'monomind', title: 'monomind checks', status: 'warn' },
    { id: 'monomind.doctor.helpers', group: 'monomind', parent: 'monomind.doctor', title: 'Helper Files', status: 'warn',
      fix: { id: 'monomind.doctor.fix:helpers', label: 'Fix helpers', safety: 'auto' } },
  ],
}

describe('groupReport', () => {
  it('keeps group order and nests children under their parent', () => {
    const groups = groupReport(report)
    expect(groups.map(g => g.group)).toEqual(['core', 'monomind', 'runtimes', 'browser'])
    const runtimes = groups.find(g => g.group === 'runtimes')
    expect(runtimes.rows).toHaveLength(1)
    expect(runtimes.rows[0].children.map(c => c.id)).toEqual(['runtimes.codex'])
    const monomind = groups.find(g => g.group === 'monomind')
    expect(monomind.problems).toBe(3) // node + doctor parent + helpers child
  })
})

describe('summarize', () => {
  it('reports a required failure as broken', () => {
    const s = summarize(report)
    expect(s.level).toBe('broken')
    expect(s.required).toBe(1)
    expect(s.issues).toBe(5)
  })
  it('is ok with no problems', () => {
    expect(summarize({ results: [{ id: 'a', status: 'ok' }] }).level).toBe('ok')
    expect(summarize(null).level).toBe('ok')
  })
})

describe('fixPlan', () => {
  it('lists auto and confirm fixes of problem rows, never manual or optional ones', () => {
    expect(fixPlan(report).map(f => f.id)).toEqual(['core.db.migrate', 'monomind.node.install', 'monomind.doctor.fix:helpers'])
  })
})

describe('versionSkew', () => {
  it('only compares release builds', () => {
    expect(versionSkew('v1.2.0', 'v1.3.0')).toBe(true)
    expect(versionSkew('v1.2.0', '1.2.0')).toBe(false)
    expect(versionSkew('dev', 'v1.3.0')).toBe(false)
    expect(versionSkew('v0.54.0-7-ged37834', 'v0.54.0')).toBe(false)
  })
})

describe('groupReport order', () => {
  it('keeps doctor\'s group order even for rows appended at the end', () => {
    const groups = groupReport({ results: [
      { id: 'core.db', group: 'core', status: 'ok' },
      { id: 'services.daemon', group: 'services', status: 'ok' },
      { id: 'runtimes.agents', group: 'runtimes', status: 'ok', carried: true },
    ] })
    expect(groups.map(g => g.group)).toEqual(['core', 'runtimes', 'services'])
  })
})

describe('healthMode and runKey', () => {
  it('maps requests to modes', () => {
    expect(healthMode()).toBe('local')
    expect(healthMode({ background: true })).toBe('background')
    expect(healthMode({ deep: true })).toBe('deep')
    expect(healthMode({ projects: true })).toBe('projects')
  })
  it('gives both installs of a runtime one key', () => {
    expect(runKey('agent.install:codex')).toBe(runKey('runtimes.install:codex'))
    expect(runKey('agent.install:codex')).not.toBe(runKey('agent.install:claude'))
    expect(runKey('core.db.migrate')).toBe('core.db.migrate')
  })
})

const row = (id, group, status = 'ok', extra = {}) => ({ id, group, title: id, status, ...extra })

describe('mergeReports', () => {
  const deep = { mode: 'deep', startedAt: 100, report: { profile_id: 'p', results: [
    row('core.db', 'core', 'fail'),
    row('monomind.doctor', 'monomind', 'warn'),
    row('monomind.doctor.helpers', 'monomind', 'warn', { parent: 'monomind.doctor', fix: { id: 'monomind.doctor.fix:helpers', safety: 'auto' } }),
    row('runtimes.agents', 'runtimes'),
    row('runtimes.codex', 'runtimes', 'info', { parent: 'runtimes.agents' }),
  ] } }
  const background = { mode: 'background', startedAt: 200, report: { profile_id: 'p', results: [row('core.db', 'core', 'ok')] } }

  it('keeps a deep report\'s extra rows (and their fixes) under a newer background check', () => {
    const merged = mergeReports([deep, background])
    const byId = Object.fromEntries(merged.results.map(r => [r.id, r]))
    expect(byId['core.db'].status).toBe('ok') // the newer run decides shared rows
    expect(byId['core.db'].carried).toBeUndefined()
    expect(byId['monomind.doctor.helpers'].carried).toBe(true)
    expect(byId['runtimes.codex'].parent).toBe('runtimes.agents')
    expect(fixPlan(merged).map(f => f.id)).toEqual(['monomind.doctor.fix:helpers'])
  })

  it('lets the newest-started run win whatever order they finish in', () => {
    // The same two runs, the older one arriving last: same result.
    expect(mergeReports([background, deep])).toEqual(mergeReports([deep, background]))
  })

  it('never carries rows from a mode the newest run covers', () => {
    const oldLocal = { mode: 'local', startedAt: 50, report: { profile_id: 'p', results: [row('core.gone', 'core', 'fail')] } }
    const merged = mergeReports([oldLocal, deep])
    expect(merged.results.find(r => r.id === 'core.gone')).toBeUndefined()
  })

  it('carries saved runtime rows into a background report, but never alone or across profiles', () => {
    const saved = { mode: 'saved', startedAt: 10, report: { profile_id: 'p', results: [row('runtimes.agents', 'runtimes', 'fail')] } }
    expect(mergeReports([saved])).toBeNull()
    expect(mergeReports([saved, background]).results.map(r => r.id)).toEqual(['core.db', 'runtimes.agents'])
    const other = { ...saved, report: { ...saved.report, profile_id: 'q' } }
    expect(mergeReports([other, background]).results.map(r => r.id)).toEqual(['core.db'])
  })

  it('lets a newer run of the parent decide its children', () => {
    const local = { mode: 'local', startedAt: 300, report: { profile_id: 'p', results: [row('runtimes.agents', 'runtimes'), row('core.db', 'core')] } }
    const merged = mergeReports([deep, local])
    expect(merged.results.find(r => r.id === 'runtimes.codex')).toBeUndefined()
    expect(merged.results.find(r => r.id === 'monomind.doctor').carried).toBe(true)
  })
})

describe('runHealth', () => {
  let health
  beforeEach(async () => {
    vi.resetModules()
    vi.clearAllMocks()
    localStorage.clear()
    health = await import('./health.js')
  })

  const report = (results, profile = 'p') => JSON.stringify({ v: 1, profile_id: profile, results })
  const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }

  it('keeps the background timer alive until the last subscriber stops (StrictMode double mount)', async () => {
    vi.useFakeTimers()
    try {
      mockRunHealthCheck.mockResolvedValue(report([row('core.db', 'core')]))
      const stopA = health.startBackgroundHealth()
      const stopB = health.startBackgroundHealth()
      stopA()
      stopA() // a repeated disposer must not release B's hold
      expect(vi.getTimerCount()).toBe(1)
      stopB()
      expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })

  it('runs the background check without the runtime scan, and keeps runtime rows from an explicit check', async () => {
    mockRunHealthCheck.mockResolvedValueOnce(report([row('core.db', 'core'), row('runtimes.agents', 'runtimes', 'fail')]))
    await health.runHealth()
    expect(mockRunHealthCheck).toHaveBeenLastCalledWith('local')
    mockRunHealthCheck.mockResolvedValueOnce(report([row('core.db', 'core', 'warn')]))
    await health.runHealth({ background: true })
    expect(mockRunHealthCheck).toHaveBeenLastCalledWith('background')
    const ids = health.getHealth().report.results.map(r => r.id)
    expect(ids).toEqual(['core.db', 'runtimes.agents'])

    // …and across an app restart (a fresh module), from storage.
    vi.resetModules()
    const fresh = await import('./health.js')
    mockRunHealthCheck.mockResolvedValueOnce(report([row('core.db', 'core')]))
    await fresh.runHealth({ background: true })
    expect(fresh.getHealth().report.results.find(r => r.id === 'runtimes.agents').carried).toBe(true)
  })

  it('does not let the background check replace a deep report that finishes later', async () => {
    const d = deferred()
    mockRunHealthCheck.mockImplementation(mode => (mode === 'deep' ? d.promise : Promise.resolve(report([row('core.db', 'core', 'ok')]))))
    const deepRun = health.runHealth({ deep: true })
    await new Promise(r => setTimeout(r, 5))
    await health.runHealth({ background: true })
    d.resolve(report([row('core.db', 'core', 'fail'), row('monomind.doctor', 'monomind', 'warn')]))
    await deepRun
    const byId = Object.fromEntries(health.getHealth().report.results.map(r => [r.id, r]))
    expect(byId['core.db'].status).toBe('ok') // the background check started later
    expect(byId['monomind.doctor'].status).toBe('warn') // the deep row is kept
  })

  it('runs a request made during the same mode\'s run again afterwards, so the latest wins', async () => {
    const first = deferred()
    mockRunHealthCheck.mockReturnValueOnce(first.promise).mockResolvedValueOnce(report([row('core.db', 'core', 'ok')]))
    const a = health.runHealth()
    const b = health.runHealth() // e.g. the re-check after a fix
    const c = health.runHealth() // coalesced with b
    first.resolve(report([row('core.db', 'core', 'fail')]))
    await a
    await Promise.all([b, c])
    expect(mockRunHealthCheck).toHaveBeenCalledTimes(2)
    expect(health.getHealth().report.results[0].status).toBe('ok')
  })

  it('cancels a running check and keeps the last report', async () => {
    mockRunHealthCheck.mockResolvedValueOnce(report([row('core.db', 'core', 'ok')]))
    await health.runHealth()
    const d = deferred()
    mockRunHealthCheck.mockReturnValueOnce(d.promise)
    mockCancel.mockResolvedValue('{"ok":true,"cancelled":true}')
    const run = health.runHealth({ deep: true })
    expect(health.getHealth().checking).toEqual(['deep'])
    await health.cancelHealthCheck()
    expect(mockCancel).toHaveBeenCalledWith('check:deep')
    d.resolve('{"error":"cancelled","cancelled":true}')
    await run
    expect(health.getHealth().error).toBeNull()
    expect(health.getHealth().loading).toBe(false)
    expect(health.getHealth().report.results[0].status).toBe('ok')
  })
})

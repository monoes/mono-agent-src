import { describe, it, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { KNOWN_PAGES, MAX_PROMPT_LENGTH, runHelpAction, describeAction } from './helpActions.js'

const mocks = () => ({ navigate: vi.fn(), openNewChat: vi.fn() })

describe('KNOWN_PAGES', () => {
  it('matches the persistentPages keys in App.jsx (minus conditional controlroom)', () => {
    const src = readFileSync(fileURLToPath(new URL('../../App.jsx', import.meta.url)), 'utf8')
    const block = src.slice(src.indexOf('const persistentPages = {'), src.indexOf('const detailPages'))
    const keys = [...block.matchAll(/^ {4}(\w+):/gm)].map(m => m[1])
    expect(keys.length).toBeGreaterThan(10)
    expect([...KNOWN_PAGES].sort()).toEqual(keys.sort())
  })
})

describe('runHelpAction goto', () => {
  it('navigates to a known page with a fresh copy of params', () => {
    const d = mocks(); const params = { section: 'jev' }
    expect(runHelpAction({ kind: 'goto', target: 'settings', params }, d)).toEqual({ ok: true })
    expect(d.navigate).toHaveBeenCalledWith('settings', { section: 'jev' })
    expect(d.navigate.mock.calls[0][1]).not.toBe(params)
  })
  it('navigates without params', () => {
    const d = mocks()
    runHelpAction({ kind: 'goto', target: 'orgs' }, d)
    expect(d.navigate).toHaveBeenCalledWith('orgs', undefined)
  })
  it('refuses an unknown page instead of falling back to the dashboard', () => {
    const d = mocks()
    const r = runHelpAction({ kind: 'goto', target: 'setings' }, d)
    expect(r.ok).toBe(false); expect(r.reason).toMatch(/unknown page/)
    expect(d.navigate).not.toHaveBeenCalled()
  })
  it('refuses when navigate is missing or params are malformed', () => {
    expect(runHelpAction({ kind: 'goto', target: 'logs' }, {}).ok).toBe(false)
    const d = mocks()
    expect(runHelpAction({ kind: 'goto', target: 'logs', params: 'x' }, d).ok).toBe(false)
    expect(d.navigate).not.toHaveBeenCalled()
  })
})

describe('runHelpAction coder', () => {
  it('opens a new chat with the trimmed prompt and setup', () => {
    const d = mocks(); const setup = { workspace: { kind: 'root' } }
    expect(runHelpAction({ kind: 'coder', prompt: '  set up jev  ', setup }, d)).toEqual({ ok: true })
    expect(d.openNewChat).toHaveBeenCalledWith({ prompt: 'set up jev', setup })
  })
  it('omits setup when absent', () => {
    const d = mocks()
    runHelpAction({ kind: 'coder', prompt: 'hi' }, d)
    expect(d.openNewChat).toHaveBeenCalledWith({ prompt: 'hi' })
  })
  it('caps the prompt length', () => {
    const d = mocks()
    runHelpAction({ kind: 'coder', prompt: 'a'.repeat(MAX_PROMPT_LENGTH + 500) }, d)
    expect(d.openNewChat.mock.calls[0][0].prompt).toHaveLength(MAX_PROMPT_LENGTH)
  })
  it('redacts secrets', () => {
    const d = mocks()
    const fakeKey = ['sk', 'x'.repeat(24)].join('-')
    runHelpAction({ kind: 'coder', prompt: `use ${fakeKey} and api_key=hunter2 now` }, d)
    const p = d.openNewChat.mock.calls[0][0].prompt
    expect(p).not.toContain(fakeKey); expect(p).not.toContain('hunter2'); expect(p).toContain('[redacted]')
  })
  it('refuses empty prompts and a missing provider', () => {
    const d = mocks()
    expect(runHelpAction({ kind: 'coder', prompt: '   ' }, d).ok).toBe(false)
    expect(runHelpAction({ kind: 'coder', prompt: 'x' }, {}).ok).toBe(false)
    expect(d.openNewChat).not.toHaveBeenCalled()
  })
})

describe('runHelpAction misc', () => {
  it('rejects bad actions', () => {
    const d = mocks()
    expect(runHelpAction(null, d).ok).toBe(false)
    expect(runHelpAction({ kind: 'nope' }, d).ok).toBe(false)
  })
})

describe('describeAction', () => {
  it('labels buttons', () => {
    expect(describeAction({ kind: 'goto', target: 'noderunner' })).toBe('Open Workflows')
    expect(describeAction({ kind: 'goto', target: 'settings' })).toBe('Open Settings')
    expect(describeAction({ kind: 'coder', prompt: 'x' })).toBe('Ask the coder')
    expect(describeAction(undefined)).toBe('Open')
  })
})

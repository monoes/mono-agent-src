import { describe, it, expect } from 'vitest'
import { missingText, monomindUnusable, monomindNeedsFix } from './useCoderMode.js'

describe('missingText', () => {
  it('a monomind that could not be run is not called outdated', () => {
    const st = { ready: false, missingCapabilities: [], probeError: 'monomind: command not found', monomindVersion: '' }
    expect(missingText(st)).toBe("needs a working monomind (monomind couldn't be used: monomind: command not found)")
    expect(missingText(st)).not.toMatch(/update/)
    expect(monomindNeedsFix(st)).toBe(true)
  })
  it('cuts a long probe error', () => {
    expect(monomindUnusable({ probeError: 'x'.repeat(500) })).toHaveLength(201)
  })
  it('cuts by code point, never inside a surrogate pair', () => {
    const cut = monomindUnusable({ probeError: 'x'.repeat(199) + '😀' + 'tail' })
    expect(cut).toBe('x'.repeat(199) + '😀…')
    expect(cut).not.toMatch(/[\ud800-\udbff](?![\udc00-\udfff])/)
  })
  it('a monomind that ran but lacks capabilities needs an update and says what was found', () => {
    const st = { ready: false, missingCapabilities: ['init-json'], probeError: '', monomindVersion: '2.1.0' }
    expect(missingText(st)).toBe('needs monomind update (found monomind 2.1.0, missing: init-json)')
    expect(monomindNeedsFix(st)).toBe(true)
  })
  it('keeps the runtime and older-CLI wording', () => {
    expect(missingText({ missingCapabilities: [], runtimes: [] })).toMatch(/needs a coding runtime/)
    expect(missingText({})).toBe('needs monomind update')
    expect(monomindNeedsFix({ missingCapabilities: [], runtimes: [] })).toBe(false)
  })
})

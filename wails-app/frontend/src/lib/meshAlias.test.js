import { describe, it, expect } from 'vitest'
import { aliasFromTitle } from './meshAlias.js'

describe('aliasFromTitle', () => {
  it('turns spaces into dashes and drops what the hub would refuse', () => {
    expect(aliasFromTitle('meta challenge')).toBe('meta-challenge')
    expect(aliasFromTitle('  Meta   Challenge  ')).toBe('Meta-Challenge')
    expect(aliasFromTitle('fix: the login (v2)!')).toBe('fix-the-login-v2')
    expect(aliasFromTitle('api_v1.2 - draft')).toBe('api_v1.2-draft')
  })
  it('starts with a letter or digit, never ends with a dash, and stops at 64 characters', () => {
    expect(aliasFromTitle('--lead dashes')).toBe('lead-dashes')
    expect(aliasFromTitle('._hidden')).toBe('hidden')
    expect(aliasFromTitle('trailing dash -')).toBe('trailing-dash')
    const long = aliasFromTitle('ab '.repeat(40))
    expect(long.length).toBeLessThanOrEqual(64)
    expect(long.endsWith('-')).toBe(false)
  })
  it('is empty when nothing is left', () => {
    expect(aliasFromTitle('🚀🚀')).toBe('')
    expect(aliasFromTitle('')).toBe('')
    expect(aliasFromTitle(undefined)).toBe('')
  })
})

import { describe, it, expect } from 'vitest'
import { runtimeLabel } from './runtimeLabels.js'

describe('runtimeLabel', () => {
  it('names dsh DeepSeek Harness and leaves other ids as they are', () => {
    expect(runtimeLabel('dsh')).toBe('DeepSeek Harness')
    expect(runtimeLabel('kilo')).toBe('Kilo Code')
    expect(runtimeLabel('cline')).toBe('cline')
    expect(runtimeLabel('claude')).toBe('claude')
  })
})

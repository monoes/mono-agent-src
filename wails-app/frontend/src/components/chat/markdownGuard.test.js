import { describe, it, expect } from 'vitest'
import { markdownTooDeep } from './markdownGuard.js'

const timed = (f) => { const t = Date.now(); const r = f(); return [r, Date.now() - t] }

describe('markdownTooDeep', () => {
  it('flags pathological nesting quickly', () => {
    for (const s of ['> '.repeat(50000), '>'.repeat(100000), '- '.repeat(40), '1. '.repeat(40), '> - '.repeat(20), `${' '.repeat(200)}- x`, `${'\t'.repeat(40)}- x`]) {
      const [r, ms] = timed(() => markdownTooDeep(s))
      expect(r).toBe(true)
      expect(ms).toBeLessThan(200)
    }
  })
  it('leaves ordinary markdown alone', () => {
    const doc = '# T\n\n> quote\n> > nested\n\n- a\n  - b\n    - c\n      - d\n1. one\n2. two\n\n```\n          code\n```\n\n    indented code\n\ntext > not a quote\n'
    expect(markdownTooDeep(doc)).toBe(false)
    expect(markdownTooDeep('> '.repeat(32) + 'x')).toBe(false)
    expect(markdownTooDeep('> '.repeat(33) + 'x')).toBe(true)
    expect(markdownTooDeep('')).toBe(false)
  })
})

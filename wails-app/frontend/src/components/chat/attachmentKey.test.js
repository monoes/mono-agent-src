import { describe, it, expect } from 'vitest'
import { attachmentKey } from './ChatAttachments'

describe('attachmentKey', () => {
  it('does not change for the others when one entry is removed', () => {
    const a = { name: 'a.png' }, b = { name: 'b.png' }, c = { name: 'c.png' }
    const before = [a, b, c]
    const after = [a, c]
    expect(attachmentKey(after, c, 1)).toBe(attachmentKey(before, c, 2))
  })
  it('keeps repeated names distinct', () => {
    const x = { name: 'x.txt' }
    const list = [x, { ...x }]
    expect(attachmentKey(list, list[0], 0)).not.toBe(attachmentKey(list, list[1], 1))
  })
})

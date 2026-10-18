import { describe, it, expect } from 'vitest'
import { quoteClaim } from './quoteClaim.js'

describe('quoteClaim', () => {
  it('quotes and escapes quotes and newlines', () => {
    expect(quoteClaim('x" and "You')).toBe('"x\\" and \\"You"')
    expect(quoteClaim('a\nb')).toBe('"a\\nb"')
  })
  it('shows invisible and direction characters as escapes', () => {
    expect(quoteClaim('ab‮cd')).toBe('"ab\\u202Ecd"')
    expect(quoteClaim('a​b')).toBe('"a\\u200Bb"')
    expect(quoteClaim('a b c')).toBe('"a\\u2028b\\u2029c"')
    expect(quoteClaim('⁦x⁩')).toBe('"\\u2066x\\u2069"')
  })
  it('caps a long claim', () => {
    const q = quoteClaim('y'.repeat(200), 10)
    expect(q).toBe('"' + 'y'.repeat(10) + '…"')
  })
  it('keeps ordinary text, accents and emoji as they are', () => {
    expect(quoteClaim('Équipe ventes 🚀')).toBe('"Équipe ventes 🚀"')
  })
  it('shows C1 controls and non-ASCII spaces as escapes, keeps the ASCII space', () => {
    expect(quoteClaim('a\u0085b')).toBe('"a\\u0085b"')
    expect(quoteClaim('a\u3000b\u00A0c')).toBe('"a\\u3000b\\u00A0c"')
    expect(quoteClaim('a b')).toBe('"a b"')
  })
  it('shows a format character above U+FFFF whole, not as \\uXXXX and a stray digit', () => {
    expect(quoteClaim('a\u{E0041}b')).toBe('"a\\u{E0041}b"')
    expect(quoteClaim('\u{E0001}x\u{E007F}')).toBe('"\\u{E0001}x\\u{E007F}"')
  })
  it('shows variation selectors, private-use characters and noncharacters as escapes', () => {
    expect(quoteClaim('a\uFE0Fb\u{E0100}c')).toBe('"a\\uFE0Fb\\u{E0100}c"')
    expect(quoteClaim('a\uE000b\u{F0000}c')).toBe('"a\\uE000b\\u{F0000}c"')
    expect(quoteClaim('a\u{10FFFF}b\uFDD0c')).toBe('"a\\u{10FFFF}b\\uFDD0c"')
  })
  it("keeps an emoji's presentation selector, escapes a stray or repeated one", () => {
    expect(quoteClaim('❤️ team')).toBe('"❤️ team"')
    expect(quoteClaim('1️⃣ first')).toBe('"1️⃣ first"')
    expect(quoteClaim('🚀\uFE0F')).toBe('"🚀\uFE0F"')
    expect(quoteClaim('a\uFE0F')).toBe('"a\\uFE0F"')
    expect(quoteClaim('❤\uFE0F\uFE0F')).toBe('"❤\uFE0F\\uFE0F"')
    expect(quoteClaim('❤\uFE0E')).toBe('"❤\\uFE0E"')
  })
  it('keeps a zero-width joiner between two emoji, escapes any other', () => {
    for (const seq of ['👨\u200D👩\u200D👧', '🏳\uFE0F\u200D🌈', '👩\u{1F3FD}\u200D💻']) {
      expect(quoteClaim(seq)).toBe(JSON.stringify(seq))
    }
    expect(quoteClaim('a\u200Db')).toBe('"a\\u200Db"')
    expect(quoteClaim('👨\u200Da')).toBe('"👨\\u200Da"')
    expect(quoteClaim('a\u200D👩')).toBe('"a\\u200D👩"')
    expect(quoteClaim('👨\u200D\u200D👩')).toBe('"👨\\u200D\\u200D👩"')
  })
  it('keeps the combining marks a script needs', () => {
    expect(quoteClaim('नमस्ते')).toBe('"नमस्ते"')
    expect(quoteClaim('e\u0301')).toBe('"e\u0301"')
  })
})

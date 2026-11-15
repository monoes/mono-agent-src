// en and es carry the same keys with the same {{tokens}}. A key that is
// intentionally identical-by-omission goes in parity.allowlist.json.
import { describe, it, expect } from 'vitest'
import en from './en.json'
import es from './es.json'
import allow from './parity.allowlist.json'

const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) =>
  (v && typeof v === 'object' ? flat(v, `${p}${k}.`) : [[`${p}${k}`, v]]))
const tokens = s => [...String(s).matchAll(/\{\{\s*([^}\s]+)\s*\}\}/g)].map(m => m[1]).sort()

const E = new Map(flat(en)), S = new Map(flat(es))
const skip = new Set(allow.keys)

describe('en/es locale parity', () => {
  it('has no key in en that es lacks', () => {
    expect([...E.keys()].filter(k => !S.has(k) && !skip.has(k))).toEqual([])
  })
  it('has no key in es that en lacks', () => {
    expect([...S.keys()].filter(k => !E.has(k))).toEqual([])
  })
  it('uses the same interpolation tokens in both', () => {
    const bad = [...E].filter(([k, v]) => S.has(k) && JSON.stringify(tokens(v)) !== JSON.stringify(tokens(S.get(k)))).map(([k]) => k)
    expect(bad).toEqual([])
  })
  it('allowlists only keys that exist in en and are missing from es', () => {
    expect(allow.keys.filter(k => !E.has(k) || S.has(k))).toEqual([])
  })
})

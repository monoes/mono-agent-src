// Every chatBlocks.* key the chat block components use exists in English and
// Spanish, and both locales carry the same chatBlocks keys.
import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? flat(v, `${p}${k}.`) : [`${p}${k}`]))
const has = (keys, k) => keys.includes(k) || (keys.includes(`${k}_one`) && keys.includes(`${k}_other`))

const dir = join(__dirname, '..', 'components', 'chat', 'blocks')
const files = readdirSync(dir).filter(f => /\.(jsx?|js)$/.test(f) && !/\.test\./.test(f))
const used = [...new Set(files.flatMap(f => [...readFileSync(join(dir, f), 'utf8').matchAll(/['"`](chatBlocks\.[a-zA-Z0-9_.]+)['"`]/g)].map(m => m[1])))]

describe('chat blocks i18n', () => {
  const enKeys = flat(en)
  const esKeys = flat(es)
  it('finds the keys the blocks use', () => { expect(used.length).toBeGreaterThan(0) })
  it('has every used key in English and Spanish', () => {
    expect(used.filter(k => !has(enKeys, k))).toEqual([])
    expect(used.filter(k => !has(esKeys, k))).toEqual([])
  })
  it('carries the same chatBlocks keys in both locales', () => {
    const s = keys => keys.filter(k => k.startsWith('chatBlocks.')).sort()
    expect(s(esKeys)).toEqual(s(enKeys))
  })
})

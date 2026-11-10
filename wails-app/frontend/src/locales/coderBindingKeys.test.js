// Every coderBinding.* key the binding toggle uses exists in English
// and Spanish, and both locales carry the same coderBinding keys.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? flat(v, `${p}${k}.`) : [`${p}${k}`]))
const source = readFileSync(join(__dirname, '..', 'components', 'settings', 'CoderPeercredBinding.jsx'), 'utf8')
const used = [...new Set([...source.matchAll(/['"`](coderBinding\.[a-zA-Z0-9_.-]+)['"`]/g)].map(m => m[1]))]

describe('coderBinding i18n', () => {
  const enKeys = flat(en)
  const esKeys = flat(es)
  it('finds the keys the toggle uses', () => { expect(used.length).toBeGreaterThan(5) })
  it('has every used key in English and Spanish', () => {
    expect(used.filter(k => !enKeys.includes(k))).toEqual([])
    expect(used.filter(k => !esKeys.includes(k))).toEqual([])
  })
  it('carries the same coderBinding keys in both locales', () => {
    const b = keys => keys.filter(k => k.startsWith('coderBinding.')).sort()
    expect(b(esKeys)).toEqual(b(enKeys))
  })
})

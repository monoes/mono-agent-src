// Every openStart.* key the open-ended start form uses exists in English and Spanish, and both carry the same keys.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'
import { LIMIT_KEYS, LIMIT_PRESETS, LINES } from '../lib/openCharter.js'

const dir = join(__dirname, '..', 'components', 'chat')
const source = ['OpenStartForm.jsx', 'OpenStartReview.jsx'].map(f => readFileSync(join(dir, f), 'utf8')).join('\n')
const literal = [...new Set([...source.matchAll(/['"`](openStart\.[a-zA-Z0-9_.]+)['"`]/g)].map(m => m[1]))]
const built = [
  ...LIMIT_KEYS.map(k => `openStart.limit.${k}`), ...Object.keys(LIMIT_PRESETS).map(k => `openStart.preset.${k}`), ...LINES.map(l => `openStart.line.${l}`),
  ...['goal', 'criteria', 'limits', 'budget', 'lines', 'boss', 'verifier', 'judging', 'perAsk'].map(k => `openStart.err.${k}`),
  ...['boss', 'verifier'].flatMap(w => ['Provider', 'Model', 'Version'].map(p => `openStart.${w}${p}`)),
]
const get = (o, k) => k.split('.').reduce((a, p) => a?.[p], o)
const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (typeof v === 'object' ? flat(v, `${p}${k}.`) : [`${p}${k}`]))

describe('openStart i18n', () => {
  it('finds the keys the form uses', () => expect(literal.length).toBeGreaterThanOrEqual(25))
  it('has every used key in English and Spanish', () => {
    for (const k of [...literal, ...built]) expect([k, typeof get(en, k), typeof get(es, k)]).toEqual([k, 'string', 'string'])
  })
  it('carries the same keys in both locales', () => expect(flat(es.openStart).sort()).toEqual(flat(en.openStart).sort()))
})

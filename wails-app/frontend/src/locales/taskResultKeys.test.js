// Every taskResult.* key the task header's result lines use exists in English
// and Spanish, and both locales carry the same taskResult keys.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? flat(v, `${p}${k}.`) : [`${p}${k}`]))
const dir = join(__dirname, '..', 'components', 'bubbles')
const source = ['FailedRunFixResult.jsx', 'TaskResultLine.jsx'].map(f => readFileSync(join(dir, f), 'utf8')).join('\n')
const literal = [...new Set([...source.matchAll(/['"`](taskResult\.[a-zA-Z0-9_.]+)['"`]/g)].map(m => m[1]))]
// t(`taskResult.warn.${key}`)
const has = (keys, k) => keys.includes(k) || (keys.includes(`${k}_one`) && keys.includes(`${k}_other`))
const used = [...literal, ...['sends', 'credential', 'expression', 'active'].map(k => `taskResult.warn.${k}`)]

describe('taskResult i18n', () => {
  const enKeys = flat(en)
  const esKeys = flat(es)
  it('finds the keys the result lines use', () => { expect(literal.length).toBeGreaterThan(15) })
  it('has every used key in English and Spanish', () => {
    expect(used.filter(k => !has(enKeys, k))).toEqual([])
    expect(used.filter(k => !has(esKeys, k))).toEqual([])
  })
  it('carries the same taskResult keys in both locales', () => {
    const b = keys => keys.filter(k => k.startsWith('taskResult.')).sort()
    expect(b(esKeys)).toEqual(b(enKeys))
  })
})

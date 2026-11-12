// The over-committed chip and the pursuit's waiting reason exist in English and Spanish with the placeholders the chip fills, and the
// mission block carries the same keys in both locales (the existing parity test covers only pursuitStart).
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const source = readFileSync(join(__dirname, '..', 'components', 'bubbles', 'mission', 'OverCommit.jsx'), 'utf8')
const used = [...new Set([...source.matchAll(/['"`](bubbles\.mission\.[a-zA-Z0-9_.]+)['"`]/g)].map(m => m[1]))]
const get = (o, k) => k.split('.').reduce((a, p) => a?.[p], o)

describe('over-committed i18n', () => {
  it('finds the keys the components use', () => expect(used.sort()).toEqual(['bubbles.mission.overCommitted', 'bubbles.mission.pursuitOverCommitted']))
  it('has every used key in English and Spanish', () => {
    for (const k of used) expect([k, typeof get(en, k), typeof get(es, k)]).toEqual([k, 'string', 'string'])
  })
  it('keeps the placeholders of the chip in both', () => {
    for (const l of [en, es]) expect(get(l, 'bubbles.mission.overCommitted')).toMatch(/\{\{usd\}\}.*\{\{by\}\}/)
  })
  it('carries the same mission keys in both locales', () => expect(Object.keys(es.bubbles.mission).sort()).toEqual(Object.keys(en.bubbles.mission).sort()))
})

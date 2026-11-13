// Every pursuitPanel.* key the panel uses exists in English and Spanish, and both carry the same keys.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const source = readFileSync(join(__dirname, '..', 'components', 'bubbles', 'mission', 'PursuitPanel.jsx'), 'utf8')
const used = [...new Set([...source.matchAll(/['"`](pursuitPanel\.[a-zA-Z0-9_.]+)['"`]/g)].map(m => m[1]))]
const get = (o, k) => k.split('.').reduce((a, p) => a?.[p], o)

describe('pursuitPanel i18n', () => {
  it('finds the keys the panel uses', () => expect(used.length).toBeGreaterThanOrEqual(15))
  it('has every used key in English and Spanish', () => {
    for (const k of used) expect([k, typeof get(en, k), typeof get(es, k)]).toEqual([k, 'string', 'string'])
  })
  it('carries the same keys in both locales', () => expect(Object.keys(es.pursuitPanel).sort()).toEqual(Object.keys(en.pursuitPanel).sort()))
})

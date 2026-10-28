// Every missionToggle.* key the Mission switch uses exists in English and Spanish.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const dir = join(__dirname, '..', 'components', 'chat')
const source = ['MissionToggle.jsx', 'useMissionToggle.js'].map(f => readFileSync(join(dir, f), 'utf8')).join('\n')
const used = [...new Set([...source.matchAll(/['"`](missionToggle\.[a-zA-Z0-9_.-]+)['"`]/g)].map(m => m[1]))]
const get = (o, k) => k.split('.').reduce((a, p) => a?.[p], o)

describe('missionToggle i18n', () => {
  it('finds the keys the switch uses', () => expect(used.length).toBeGreaterThanOrEqual(8))
  it('has every used key in English and Spanish', () => {
    expect(used.filter(k => typeof get(en, k) !== 'string')).toEqual([])
    expect(used.filter(k => typeof get(es, k) !== 'string')).toEqual([])
  })
  it('carries the same keys in both locales', () => {
    expect(Object.keys(es.missionToggle).sort()).toEqual(Object.keys(en.missionToggle).sort())
  })
})

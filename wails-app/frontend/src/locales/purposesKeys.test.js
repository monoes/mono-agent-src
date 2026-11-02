// Every settings.purposes.* key the Agent purposes pane uses exists in English and Spanish, with the same keys in both.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'
import contract from '../components/settings/__fixtures__/agentPurposes.contract.json'

const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? flat(v, `${p}${k}.`) : [`${p}${k}`]))
const dir = join(__dirname, '..', 'components', 'settings')
const src = ['AgentPurposesSection.jsx', 'PurposeCard.jsx', 'PairChips.jsx'].map(f => readFileSync(join(dir, f), 'utf8')).join('\n')
const used = [...new Set([...src.matchAll(/['"`](settings\.purposes\.[a-zA-Z0-9_.]+)['"`]/g)].map(m => m[1]))]

describe('settings.purposes i18n', () => {
  const enKeys = flat(en), esKeys = flat(es)
  const mine = keys => keys.filter(k => k.startsWith('settings.purposes.')).sort()
  it('has every used key in both locales, and the same keys', () => {
    expect(used.length).toBeGreaterThan(30)
    expect(used.filter(k => !enKeys.includes(k))).toEqual([])
    expect(used.filter(k => !esKeys.includes(k))).toEqual([])
    expect(mine(esKeys)).toEqual(mine(enKeys))
  })
  it('names and describes every purpose, rule, fallback and status', () => {
    const want = [
      ...contract.purposes.flatMap(p => [`settings.purposes.names.${p.id}`, `settings.purposes.descriptions.${p.id}`]),
      ...['any', 'order', 'cheapest'].map(r => `settings.purposes.rules.${r}`),
      ...['any', 'none'].map(r => `settings.purposes.fallbacks.${r}`),
      ...['ok', 'stale', 'failed', 'unvalidated'].map(r => `settings.purposes.status.${r}`),
    ]
    expect(want.filter(k => !enKeys.includes(k))).toEqual([])
    expect(want.filter(k => !esKeys.includes(k))).toEqual([])
  })
  it('does not leave an English value in the Spanish file', () => {
    const val = (o, k) => k.split('.').reduce((a, x) => a[x], o)
    const same = mine(esKeys).filter(k => val(en, k) === val(es, k) && /[a-z]{4,}/.test(val(en, k)))
    expect(same).toEqual([])
  })
})

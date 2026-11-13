// Every pursuitStart.* key the pursuit start form uses exists in English and Spanish, and both carry the same keys.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const source = readFileSync(join(__dirname, '..', 'components', 'bubbles', 'mission', 'PursuitStartForm.jsx'), 'utf8') +
  readFileSync(join(__dirname, '..', 'components', 'bubbles', 'mission', 'MissionStrip.jsx'), 'utf8')
const literal = [...new Set([...source.matchAll(/['"`](pursuitStart\.[a-zA-Z0-9_.]+)['"`]/g)].map(m => m[1]))]
const built = [
  ...['operator_only', 'invalid_input', 'busy', 'token', 'phrase', 'blocked', 'declined', 'no_native_dialog', 'consent_stale', 'stale', 'chat_mismatch',
    'hidden_characters', 'criterion_too_long', 'already_started', 'unknown'].map(c => `pursuitStart.code.${c}`),
  ...['goal_too_long', 'unreadable', 'busy', 'rate_limited', 'operator_only'].map(c => `pursuitStart.previewCode.${c}`),
  ...['parkedMax', 'pct', 'wakesMax'].map(k => `pursuitStart.limit.${k}`),
  ...['awayPct', 'awayParked', 'awayWakes', 'checkerCommands'].flatMap(k => [`pursuitStart.${k}_one`, `pursuitStart.${k}_other`]),
]
const get = (o, k) => k.split('.').reduce((a, p) => a?.[p], o)
const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (typeof v === 'object' ? flat(v, `${p}${k}.`) : [`${p}${k}`]))
// plural keys are used as `pursuitStart.awayPct` and stored as _one/_other
const plural = k => /_(one|other)$/.test(k)

describe('pursuitStart i18n', () => {
  it('finds the keys the form uses', () => expect(literal.length).toBeGreaterThanOrEqual(20))
  it('has every used key in English and Spanish', () => {
    for (const k of [...literal.filter(k => !['pursuitStart.awayPct', 'pursuitStart.awayParked', 'pursuitStart.awayWakes', 'pursuitStart.checkerCommands'].includes(k)), ...built]) {
      expect([k, typeof get(en, k), typeof get(es, k)]).toEqual([k, 'string', 'string'])
    }
    expect(built.some(plural)).toBe(true)
  })
  it('carries the same keys in both locales', () => expect(flat(es.pursuitStart).sort()).toEqual(flat(en.pursuitStart).sort()))
})

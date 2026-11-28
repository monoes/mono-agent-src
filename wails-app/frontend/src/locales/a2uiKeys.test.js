// Every a2ui.* key the card renderer and its hooks use exists in English and
// Spanish, including the tone names built from a badge's tone.
import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const dir = join(__dirname, '..', 'components', 'a2ui')
const sources = readdirSync(dir).filter(f => /\.jsx?$/.test(f) && !/test|fixtures/.test(f))
const literal = [...new Set(sources.flatMap(f => [...readFileSync(join(dir, f), 'utf8').matchAll(/'(a2ui\.[a-zA-Z]+)'/g)].map(m => m[1])))]
const tones = ['info', 'success', 'warning', 'danger'].map(t => `a2ui.tone.${t}`)
const get = (o, k) => k.split('.').reduce((v, p) => v?.[p], o)

describe('a2ui card i18n', () => {
  it('finds the keys the cards use', () => {
    expect(literal.length).toBeGreaterThan(18)
  })
  it('has every used key in English and Spanish', () => {
    for (const k of [...literal, ...tones]) {
      expect(get(en, k), `en ${k}`).toBeTruthy()
      expect(get(es, k), `es ${k}`).toBeTruthy()
    }
  })
})

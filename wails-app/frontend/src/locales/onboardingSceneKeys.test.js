// Every onboarding.scene.* key the village uses exists in English and Spanish,
// and both locales carry the same onboarding.scene keys.
import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? flat(v, `${p}${k}.`) : [`${p}${k}`]))

const root = join(__dirname, '..', 'onboarding')
const sources = [join(root, 'Scene.jsx'), ...readdirSync(join(root, 'scene')).filter(f => /\.jsx?$/.test(f) && !f.includes('.test.')).map(f => join(root, 'scene', f))]
const used = [...new Set(sources.flatMap(f => [...readFileSync(f, 'utf8').matchAll(/['"`](onboarding\.scene\.[a-zA-Z0-9_.]+)['"`]/g)].map(m => m[1])))]

describe('onboarding scene i18n', () => {
  const enKeys = flat(en), esKeys = flat(es)
  it('finds the keys the scene uses', () => { expect(used.length).toBe(12) })
  it('has every used key in English and Spanish', () => {
    expect(used.filter(k => !enKeys.includes(k))).toEqual([])
    expect(used.filter(k => !esKeys.includes(k))).toEqual([])
  })
  it('carries the same onboarding.scene keys in both locales, all of them used', () => {
    const scene = keys => keys.filter(k => k.startsWith('onboarding.scene.')).sort()
    expect(scene(esKeys)).toEqual(scene(enKeys))
    expect(scene(enKeys)).toEqual([...used].sort())
  })
})

// The onboarding's strings: every key under `onboarding` in en.json is in
// es.json, no value in either is empty, and the brand is spelled MonoAgent
// (a path such as ~/.monoagent or the monoagentcli command is not the brand).
import { describe, it, expect } from 'vitest'
import en from '../locales/en.json'
import es from '../locales/es.json'

// leaves flattens an object into [dotted key, value] pairs.
function leaves(obj, prefix = '') {
  return Object.entries(obj).flatMap(([k, v]) => {
    const key = prefix ? `${prefix}.${k}` : k
    return v && typeof v === 'object' ? leaves(v, key) : [[key, v]]
  })
}

const MISSPELT = /Mono Agent|Monoagent|monoAgent|(?<![.\w/~-])monoagent(?!cli|\.db|[\w/-])/

describe('onboarding locale strings', () => {
  const enLeaves = leaves(en.onboarding, 'onboarding')
  const esLeaves = new Map(leaves(es.onboarding || {}, 'onboarding'))

  it('has an onboarding section in en.json', () => {
    expect(enLeaves.length).toBeGreaterThan(0)
  })

  it('has every onboarding key of en.json in es.json', () => {
    const missing = enLeaves.map(([k]) => k).filter((k) => !esLeaves.has(k))
    expect(missing).toEqual([])
  })

  it('has no empty onboarding value in either file', () => {
    const empty = [...enLeaves, ...esLeaves].filter(([, v]) => typeof v !== 'string' || v.trim() === '').map(([k]) => k)
    expect(empty).toEqual([])
  })

  it('spells the brand MonoAgent', () => {
    const bad = [...enLeaves, ...esLeaves].filter(([, v]) => MISSPELT.test(String(v))).map(([k, v]) => `${k}: ${v}`)
    expect(bad).toEqual([])
  })

  it('the brand check catches a misspelling and spares paths and the CLI', () => {
    for (const s of ['Mono Agent', 'Monoagent sets up', 'by monoAgent', 'monoagent needs']) expect(MISSPELT.test(s)).toBe(true)
    for (const s of ['MonoAgent', '~/.monoagent/extension', 'run monoagentcli doctor', 'monoagent.db']) expect(MISSPELT.test(s)).toBe(false)
  })
})

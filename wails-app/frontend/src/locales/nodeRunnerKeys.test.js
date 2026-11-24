// Every nodeRunner.* key the workflow editor asks for exists in en and es.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import en from './en.json'
import es from './es.json'

const FILES = ['../pages/NodeRunner.jsx', '../pages/NodeRunnerModals.jsx', '../pages/NodeInspectorWidgets.jsx']
const used = [...new Set(FILES.flatMap(f =>
  [...readFileSync(new URL(f, import.meta.url), 'utf8').matchAll(/\bt\(\s*'(nodeRunner\.[A-Za-z0-9_.]+)'/g)].map(m => m[1])))]
const get = (o, k) => k.split('.').reduce((a, p) => (a == null ? a : a[p]), o)

describe('nodeRunner keys', () => {
  it('finds the keys the editor uses', () => { expect(used.length).toBeGreaterThan(20) })
  it('resolves every used key in en and es', () => {
    expect(used.filter(k => typeof get(en, k) !== 'string')).toEqual([])
    expect(used.filter(k => typeof get(es, k) !== 'string')).toEqual([])
  })
  it('has no key en/es that the editor never asks for', () => {
    const flat = (o, p) => Object.entries(o).flatMap(([k, v]) => (typeof v === 'object' ? flat(v, `${p}.${k}`) : [`${p}.${k}`]))
    expect(flat(en.nodeRunner, 'nodeRunner').filter(k => !used.includes(k))).toEqual([])
  })
})

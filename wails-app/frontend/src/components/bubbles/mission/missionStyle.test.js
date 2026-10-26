// Every custom property the mission styles read must be defined in index.css:
// an undefined one makes the declaration invalid (the template menu was drawn
// with no background and its items showed over the setup panel behind it).
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const read = (rel) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8')
const index = read('../../../index.css')
const defined = new Set([...index.matchAll(/(--[\w-]+)\s*:/g)].map(m => m[1]))

describe('mission.css', () => {
  it('uses only custom properties that exist', () => {
    const used = [...read('./mission.css').matchAll(/var\(\s*(--[\w-]+)/g)].map(m => m[1])
    expect(used.filter(v => !defined.has(v))).toEqual([])
  })
})

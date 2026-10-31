// The purposes service may only call Wails methods that exist in the
// generated bindings: App.js (runtime) and App.d.ts (types). Both plain
// GoApp.X() and optional GoApp.X?.() calls count.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import * as GoApp from '../wailsjs/go/main/App.js'

const here = (p) => fileURLToPath(new URL(p, import.meta.url))
const apiSrc = readFileSync(here('./api.js'), 'utf8')
const dts = readFileSync(here('../wailsjs/go/main/App.d.ts'), 'utf8')

const used = [...new Set([...apiSrc.matchAll(/GoApp\.(AgentPurpose\w*)\s*(?:\?\.)?\(/g)].map((m) => m[1]))]

describe('agent purposes bindings', () => {
  it('the service uses the three purposes methods', () => {
    expect(used.sort()).toEqual(['AgentPurposeReset', 'AgentPurposeSet', 'AgentPurposesList'])
  })
  it.each(used)('%s exists in App.js and App.d.ts', (name) => {
    expect(typeof GoApp[name]).toBe('function')
    expect(dts).toMatch(new RegExp(`export function ${name}\\(`))
  })
})

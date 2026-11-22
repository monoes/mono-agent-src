// SURFACE_KINDS (jevShared.jsx) is jevconf.SurfaceKinds, read here from the
// Go source: a surface or a kind added there fails this until the table in
// the frontend follows. The GUI disables a provider in a feature's select by
// it, so a stale table would offer a provider the CLI then refuses.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { SURFACE_KINDS } from './jevShared.jsx'

const src = readFileSync(fileURLToPath(new URL('../../../../../internal/jev/jevconf/jevconf.go', import.meta.url)), 'utf8')

describe('SURFACE_KINDS', () => {
  it('matches jevconf.SurfaceKinds', () => {
    const names = Object.fromEntries([...src.matchAll(/^\s*(\w+)\s+Surface\s*=\s*"([^"]+)"/gm)].map(m => [m[1], m[2]]))
    const kinds = { 'jev.TypeChoice': 'choice', 'jev.TypeNoul': 'noul', 'jev.TypeScore': 'score' }
    const block = /var SurfaceKinds = map\[Surface\]\[\]string\{([\s\S]*?)\n\}/.exec(src)[1]
    const fromGo = Object.fromEntries([...block.matchAll(/^\s*(\w+):\s*\{([^}]*)\}/gm)].map(m =>
      [names[m[1]], m[2].split(',').map(k => k.trim()).filter(Boolean).map(k => kinds[k])]))
    expect(Object.keys(fromGo).length).toBeGreaterThan(8)
    expect(Object.values(fromGo).flat()).not.toContain(undefined)
    expect(SURFACE_KINDS).toEqual(fromGo)
  })
})

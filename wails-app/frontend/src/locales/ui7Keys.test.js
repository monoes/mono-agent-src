// Every ui7* key the source asks for exists in en and es, and no ui7* key is left unused.
// Keys are written out as 'ui7X.key' strings, never built. (Tokens: parity.test.js.)
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const SRC = fileURLToPath(new URL('..', import.meta.url))
const sources = readdirSync(SRC, { recursive: true })
  .map(String)
  .filter(f => /\.jsx?$/.test(f) && !/\.test\.jsx?$/.test(f) && !f.startsWith('locales') && !f.split('/').pop().startsWith('._'))
  .map(f => readFileSync(join(SRC, f), 'utf8'))
  .join('\n')

const used = [...new Set([...sources.matchAll(/['"`](ui7[A-Za-z0-9]*\.[A-Za-z0-9_]+)(?=['"`])/g)].map(m => m[1]))]
const base = k => k.replace(/_(zero|one|two|few|many|other)$/, '')
const keysOf = lang => Object.entries(lang)
  .filter(([ns]) => ns.startsWith('ui7'))
  .flatMap(([ns, v]) => Object.keys(v).map(k => `${ns}.${k}`))

describe('ui7 locale keys', () => {
  it('finds keys in use', () => { expect(used.length).toBeGreaterThan(0) })
  it.each([['en', en], ['es', es]])('resolves every used key in %s', (_, lang) => {
    const all = keysOf(lang)
    const have = new Set([...all, ...all.map(base)])
    expect(used.filter(k => !have.has(k))).toEqual([])
  })
  it('keeps no unused key', () => {
    const asked = new Set(used)
    expect(keysOf(en).filter(k => !asked.has(k) && !asked.has(base(k)))).toEqual([])
  })
  it('has the same keys in en and es', () => {
    expect(keysOf(es).sort()).toEqual(keysOf(en).sort())
  })
})

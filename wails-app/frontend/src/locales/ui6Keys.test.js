// Every key in the ui6* namespaces that the source asks for exists in en and es, and no ui6* key
// is left unused. (Equal tokens across languages are checked by parity.test.js.)
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

// Keys written out in t('ns.key') calls or kept as 'ns.key' strings in lookup tables.
const used = [...new Set([...sources.matchAll(/['"`](ui6[A-Za-z0-9]*\.[A-Za-z0-9_]+)(?=['"`])/g)].map(m => m[1]))]
// Keys built as `ns.prefix_${value}`.
const prefixes = [...new Set([...sources.matchAll(/`(ui6[A-Za-z0-9]*\.[A-Za-z0-9_]*)\$\{/g)].map(m => m[1]))]
const base = k => k.replace(/_(zero|one|two|few|many|other)$/, '')
const keysOf = lang => Object.entries(lang)
  .filter(([ns]) => ns.startsWith('ui6'))
  .flatMap(([ns, v]) => Object.keys(v).map(k => `${ns}.${k}`))

describe('ui6 locale keys', () => {
  it('finds keys in use', () => { expect(used.length).toBeGreaterThan(0) })
  it.each([['en', en], ['es', es]])('resolves every used key in %s', (_, lang) => {
    const all = keysOf(lang)
    const have = new Set([...all, ...all.map(base)])
    expect(used.filter(k => !have.has(k))).toEqual([])
    expect(prefixes.filter(p => !all.some(k => k.startsWith(p)))).toEqual([])
  })
  it('keeps no unused key', () => {
    const asked = new Set(used)
    expect(keysOf(en).filter(k => !asked.has(k) && !asked.has(base(k)) && !prefixes.some(p => k.startsWith(p)))).toEqual([])
  })
  it('has the same keys in en and es', () => {
    expect(keysOf(es).sort()).toEqual(keysOf(en).sort())
  })
})

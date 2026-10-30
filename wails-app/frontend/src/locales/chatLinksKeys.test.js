// Every chatLinks.* key the chat link/path components use exists in English and Spanish.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? flat(v, `${p}${k}.`) : [`${p}${k}`]))
const dir = join(__dirname, '..', 'components', 'chat')
const src = ['ChatMarkdown.jsx', 'ChatPathChip.jsx', 'ChatFilePreview.jsx'].map(f => readFileSync(join(dir, f), 'utf8')).join('\n')
const used = [...new Set([...src.matchAll(/['"`](chatLinks\.[a-zA-Z0-9_.]+)['"`]/g)].map(m => m[1]))]

describe('chatLinks i18n', () => {
  it('has every key in both locales, and the same keys', () => {
    const enKeys = flat(en), esKeys = flat(es)
    expect(used.length).toBeGreaterThan(10)
    expect(used.filter(k => !enKeys.includes(k))).toEqual([])
    expect(used.filter(k => !esKeys.includes(k))).toEqual([])
    const a = keys => keys.filter(k => k.startsWith('chatLinks.')).sort()
    expect(a(esKeys)).toEqual(a(enKeys))
  })
})

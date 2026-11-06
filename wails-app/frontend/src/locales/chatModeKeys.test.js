// Every chatMode.* key the chat mode selector uses exists in English and Spanish.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const dir = join(__dirname, '..', 'components', 'chat')
const source = ['ChatModeSelector.jsx', 'useChatMode.js'].map(f => readFileSync(join(dir, f), 'utf8')).join('\n')
const used = [...new Set([...source.matchAll(/['"`](chatMode\.[a-zA-Z0-9_.-]+)['"`]/g)].map(m => m[1]))]
const get = (o, k) => k.split('.').reduce((a, p) => a?.[p], o)

describe('chatMode i18n', () => {
  it('finds the keys the switch uses', () => expect(used.length).toBeGreaterThanOrEqual(6))
  it('has the three mode names the selector builds its keys from', () => {
    for (const m of ['off', 'mission', 'open']) expect([typeof get(en, `chatMode.${m}`), typeof get(es, `chatMode.${m}`)]).toEqual(['string', 'string'])
  })
  it('has every used key in English and Spanish', () => {
    expect(used.filter(k => typeof get(en, k) !== 'string')).toEqual([])
    expect(used.filter(k => typeof get(es, k) !== 'string')).toEqual([])
  })
  it('carries the same keys in both locales', () => {
    expect(Object.keys(es.chatMode).sort()).toEqual(Object.keys(en.chatMode).sort())
  })
})

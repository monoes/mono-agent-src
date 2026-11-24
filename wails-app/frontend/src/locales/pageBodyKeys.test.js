// Every key the Logs / Profile / Vault / Media Vault / node-inspector pages ask for in their own
// body namespaces exists in en and es, and no key in those namespaces is left unused.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import en from './en.json'
import es from './es.json'

const PAGES = {
  logsBody: ['../pages/Logs.jsx'],
  profileBody: ['../pages/Profile.jsx'],
  vaultBody: ['../pages/Vault.jsx'],
  mediaVaultBody: ['../pages/MediaVault.jsx'],
  nodeRunnerMore: ['../pages/NodeRunner.jsx', '../pages/NodeRunnerModals.jsx', '../pages/NodeInspectorWidgets.jsx'],
}
const flat = (o, p) => Object.entries(o).flatMap(([k, v]) => (typeof v === 'object' ? flat(v, `${p}.${k}`) : [`${p}.${k}`]))
const base = k => k.replace(/_(one|other)$/, '')

describe.each(Object.entries(PAGES))('%s keys', (ns, files) => {
  const src = files.map(f => readFileSync(new URL(f, import.meta.url), 'utf8')).join('\n')
  // Keys written out in t('ns.key') calls, plus ns.key strings that sit in lookup tables.
  const used = [...new Set([...src.matchAll(new RegExp(`['\`](${ns}\\.[A-Za-z0-9_.]+)['\`]`, 'g'))].map(m => m[1]))]
  const dynamic = [...src.matchAll(new RegExp(`\`${ns}\\.([A-Za-z0-9_]+)\\$\\{`, 'g'))].map(m => m[1])
  it('uses its namespace', () => { expect(used.length + dynamic.length).toBeGreaterThan(0) })
  it('resolves every used key in en and es', () => {
    for (const lang of [en, es]) {
      const have = new Set(flat(lang[ns] || {}, ns).map(base))
      expect(used.filter(k => !have.has(k))).toEqual([])
    }
  })
  it('keeps no unused key and the same keys in en and es', () => {
    const enKeys = flat(en[ns], ns).map(base)
    expect(flat(es[ns], ns).map(base)).toEqual(enKeys)
    expect(enKeys.filter(k => !used.includes(k) && !dynamic.some(d => k.startsWith(`${ns}.${d}`)))).toEqual([])
  })
})

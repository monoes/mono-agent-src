// Every personItem.* key the answer card, the views and Settings use exists in English and
// Spanish, and both locales carry the same keys.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? flat(v, `${p}${k}.`) : [`${p}${k}`]))
const has = (keys, k) => keys.includes(k) || (keys.includes(`${k}_one`) && keys.includes(`${k}_other`))
const files = ['components/bubbles/PersonItemCard.jsx', 'components/bubbles/AskItemCard.jsx', 'components/bubbles/PersonItemViews.jsx', 'components/bubbles/PersonItemsContext.jsx', 'components/settings/NotificationsSection.jsx', 'components/bubbles/OrbitRing.jsx']
const root = join(__dirname, '..')
const used = [...new Set(files.flatMap(f => [...readFileSync(join(root, f), 'utf8').matchAll(/['"`]((?:personItem|bubbles\.orbit)\.[a-zA-Z0-9_.]+)['"`]/g)].map(m => m[1])))]

describe('personItem i18n', () => {
  it('finds the keys', () => expect(used.length).toBeGreaterThan(20))
  it('has every used key in both locales', () => {
    expect(used.filter(k => !has(flat(en), k))).toEqual([])
    expect(used.filter(k => !has(flat(es), k))).toEqual([])
  })
  it('carries the same personItem keys in both locales', () => {
    const p = keys => keys.filter(k => k.startsWith('personItem.')).sort()
    expect(p(flat(es))).toEqual(p(flat(en)))
  })
  it('words the notification and the tray for every kind of item, with no plan or question text', () => {
    for (const loc of [en, es]) {
      for (const k of ['notifyBody_one', 'notifyBody_other', 'trayLabel_one', 'trayLabel_other']) {
        expect(loc.personItem[k]).not.toMatch(/\bplan|plane|aprobaci/i)
        expect(loc.personItem[k].match(/\{\{\w+\}\}/g)).toEqual(['{{count}}'])
      }
    }
  })
})

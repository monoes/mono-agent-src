import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { PAGE_IDS, isNavigablePage } from './pageIds.js'
import { TOPICS } from '../pages/Help/helpContent.js'
import { KNOWN_PAGES } from '../pages/Help/helpActions.js'

const appSrc = readFileSync(fileURLToPath(new URL('../App.jsx', import.meta.url)), 'utf8')

describe('pageIds', () => {
  it('accepts every page id and the conditional/detail pages, refuses the rest', () => {
    for (const id of PAGE_IDS) expect(isNavigablePage(id), id).toBe(true)
    for (const id of ['controlroom', 'profile', 'postDetail']) expect(isNavigablePage(id), id).toBe(true)
    for (const id of ['', 'dashbord', undefined, null, 'constructor', '__proto__']) expect(isNavigablePage(id), String(id)).toBe(false)
  })

  it('helpActions shares the one list instead of keeping its own', () => {
    expect(KNOWN_PAGES).toBe(PAGE_IDS)
  })

  it('App.jsx re-exports PAGE_IDS and navigate() refuses unknown ids before touching state', () => {
    expect(appSrc).toMatch(/export \{ PAGE_IDS \}/)
    const nav = appSrc.slice(appSrc.indexOf('const navigate = useCallback'))
    const guard = nav.indexOf('isNavigablePage(page)')
    expect(guard).toBeGreaterThan(-1)
    expect(guard).toBeLessThan(nav.indexOf('setNavData('))
    expect(guard).toBeLessThan(nav.indexOf('setActivePage(page)'))
  })

  it('navigate() hands pages a fresh navData object each call', () => {
    expect(appSrc).toMatch(/setNavData\(data \? \{ \.\.\.data \} : null\)/)
  })

  it('every Help goto target is a navigable page', () => {
    for (const t of TOPICS) for (const s of t.steps) {
      if (s.action?.kind === 'goto') expect(isNavigablePage(s.action.target), `${t.id}: ${s.action.target}`).toBe(true)
    }
  })
})

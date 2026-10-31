// The right-hand bubble dock is fixed over the window's right edge, where the
// task drawer ends: the drawer must keep a column clear of it.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('./bubbles.css', import.meta.url)), 'utf8')
const px = (re) => Number(css.match(re)?.[1] ?? NaN)

describe('bubble dock beside the task drawer', () => {
  it('reserves at least the dock footprint (offset + bubble) on the drawer\'s right', () => {
    const reserve = px(/body:has\(\.bubble-dock\.right\)\s+\.tb-drawer\s*\{\s*margin-right:\s*(\d+)px/)
    const offset = px(/\.bubble-dock\.right\s*\{\s*right:\s*(\d+)px/)
    const bubble = px(/\n\.bubble\s*\{[^}]*?width:\s*(\d+)px/)
    expect(reserve).toBeGreaterThanOrEqual(offset + bubble)
  })
  it('keeps an orbit (about 100px more) clear of the drawer, the inbox and an expanded pane, and drops it from a full-width pane', () => {
    expect(css).toMatch(/\.bubble-dock\.right \.has-orbit\) \.tb-drawer \{ margin-right: 150px/)
    expect(css).toMatch(/\.bubble-dock\.right\) \.hil-body \{ padding-right: 76px/)
    expect(css).toMatch(/\.bubble-dock\.right \.has-orbit\) \.hil-body \{ padding-right: 150px/)
    expect(css).toMatch(/\.bubble-dock\.right \.has-orbit\) \.bubble-overlay:not\(\.narrow\) \{ width: min\(1080px, calc\(100vw - 300px\)\)/)
    expect(css).toMatch(/\.bubble-overlay\.narrow\) \.bubble-dock \.orbit-arc \{ display: none/)
  })
})

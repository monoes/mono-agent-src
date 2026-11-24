// The hero empty state is a new class, not an override of the faint default.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('./index.css', import.meta.url)), 'utf8')
const rule = (sel) => css.match(new RegExp(`${sel.replace(/[.\s-]/g, c => '\\' + c)}\\s*\\{([^}]*)\\}`))?.[1] || ''

describe('.empty-state--hero', () => {
  it('shows the icon at full strength and allows a longer description', () => {
    expect(rule('.empty-state--hero .empty-state-icon')).toMatch(/opacity:\s*1\b/)
    expect(Number(rule('.empty-state--hero .empty-state-desc').match(/max-width:\s*(\d+)px/)?.[1])).toBeGreaterThan(280)
  })
  it('leaves the default empty state faint and narrow', () => {
    expect(rule('.empty-state-icon')).toMatch(/opacity:\s*0\.3/)
    expect(rule('.empty-state-desc')).toMatch(/max-width:\s*280px/)
  })
})

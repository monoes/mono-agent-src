// With the Human-in-Loop panel open the page headers are narrow: the controls
// must wrap below the title, not squeeze the subtitle to a word a line. jsdom
// lays nothing out, so this pins the rules that do it.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('./index.css', import.meta.url)), 'utf8')
const rule = (name) => css.match(new RegExp(`\\n\\${name} \\{([^}]*)\\}`))?.[1] || ''

describe('page headers', () => {
  it('wrap their controls and grow instead of keeping a fixed height', () => {
    expect(rule('.page-header')).toMatch(/flex-wrap: wrap/)
    expect(rule('.page-header')).toMatch(/min-height: 52px/)
    expect(rule('.page-header')).not.toMatch(/\n\s*height:/)
    expect(rule('.page-header-right')).toMatch(/flex-wrap: wrap/)
  })
  it('keep room for the title side before the controls take it', () => {
    expect(rule('.page-header-left')).toMatch(/flex: 1 1 260px/)
    expect(rule('.page-header-left')).toMatch(/min-width: 0/)
  })
  it('never break a title and truncate a long subtitle', () => {
    expect(rule('.page-title')).toMatch(/white-space: nowrap/)
    for (const want of [/white-space: nowrap/, /overflow: hidden/, /text-overflow: ellipsis/, /min-width: 0/]) {
      expect(rule('.page-subtitle')).toMatch(want)
    }
  })
})

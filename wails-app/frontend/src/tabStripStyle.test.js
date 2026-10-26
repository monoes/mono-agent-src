// The session tab strip scrolls sideways (overflow-x: auto), which also clips
// vertically: a focus ring (2px outline + 2px offset) and the selected tab's
// glow need room on top and at the sides inside the strip, or they are cut off.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('./index.css', import.meta.url)), 'utf8')

describe('.ts-tabs', () => {
  it('pads the strip so a focused tab keeps its whole ring', () => {
    const rule = css.match(/\.ts-tabs\s*\{([^}]*)\}/)?.[1] || ''
    const pad = rule.match(/padding:\s*(\d+)px(?:\s+(\d+)px)?/)
    expect(pad, 'a padding on .ts-tabs').toBeTruthy()
    expect(Number(pad[1])).toBeGreaterThanOrEqual(4)
    expect(Number(pad[2] ?? pad[1])).toBeGreaterThanOrEqual(4)
  })
})

// WCAG contrast of the empty-column text (12px: needs 4.5:1) on the surfaces it sits on.
const token = (name) => css.match(new RegExp(`${name}:\\s*(#[0-9a-fA-F]{6})`))?.[1]
const lum = (hex) => {
  const [r, g, b] = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map(c => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
const ratio = (a, b) => { const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x); return (hi + 0.05) / (lo + 0.05) }

describe('.tb-empty', () => {
  it('reads at 4.5:1 or more on the column and plan surface', () => {
    const rule = css.match(/\.tb-empty\s*\{([^}]*)\}/)?.[1] || ''
    const colour = rule.match(/color:\s*var\((--[\w-]+)\)/)?.[1]
    expect(colour, 'a colour token on .tb-empty').toBeTruthy()
    for (const bg of ['--surface', '--elevated']) {
      expect(ratio(token(colour), token(bg)), `${colour} on ${bg}`).toBeGreaterThanOrEqual(4.5)
    }
  })
})

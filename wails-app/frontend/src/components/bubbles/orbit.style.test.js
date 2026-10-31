// The orbit's motion rules, read from its stylesheet.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

describe('orbit styles', () => {
  const css = readFileSync(fileURLToPath(new URL('./orbit.css', import.meta.url)), 'utf8')
  it('turns every animation and transition off under prefers-reduced-motion', () => {
    const block = css.slice(css.indexOf('@media (prefers-reduced-motion: reduce)'))
    for (const sel of ['.orbit-sat', '.orbit-chip', '.orbit-link', '.orbit-pulse', '.orbit-face']) expect(block).toContain(sel)
    expect(block).toMatch(/animation:\s*none\s*!important/)
    expect(block).toMatch(/transition:\s*none\s*!important/)
  })
  it('animates transform and opacity only', () => {
    const frames = [...css.matchAll(/@keyframes\s+\w+\s*\{(?:[^{}]*\{[^{}]*\})+[^{}]*\}/g)].map(m => m[0])
    expect(frames.length).toBeGreaterThan(2)
    for (const f of frames) expect(f).not.toMatch(/\b(width|height|top|left|right|bottom|margin|padding)\s*:/)
  })
  it('gives a stopped agent its own neutral ring, dotted, apart from failed and paused', () => {
    expect(css).toMatch(/\.orbit-sat\[data-state="stopped"\]\s*\{\s*--ring: var\(--orbit-stopped\)/)
    expect(css).toMatch(/\.orbit-sat\[data-state="stopped"\] \.orbit-face\s*\{\s*border-style: dotted/)
    expect(css).toMatch(/--orbit-stopped: var\(--text-secondary\)/)
  })
  it('uses the bubble tokens, no hard-coded hex colours', () => {
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
  })
})

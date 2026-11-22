// Every gap class the coder host accepts (internal/coderhost GapClasses) has a
// label in the session disclosure, in English and Spanish.
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import en from './en.json'
import es from './es.json'

const src = readFileSync(join(__dirname, '..', '..', '..', '..', 'internal', 'coderhost', 'settings.go'), 'utf8')
const line = src.match(/GapClasses\s*=\s*\[\]string\{([^}]*)\}/)
const classes = line ? [...line[1].matchAll(/"([a-z_]+)"/g)].map(m => m[1]) : []

describe('bubbles.pane.session.class', () => {
  it('reads the gap classes from the Go source', () => {
    expect(classes.length).toBeGreaterThan(5)
  })
  it.each([['en', en], ['es', es]])('labels every gap class in %s', (_, loc) => {
    const labels = loc.bubbles.pane.session.class
    expect(classes.filter(c => !labels[c])).toEqual([])
  })
})

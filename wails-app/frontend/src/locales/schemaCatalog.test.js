// Every text of a workflow-step setting that a bundled node schema (internal/workflow/schemas)
// or the editor's fallback list (pages/nodeConfigFields.js) shows has a Spanish entry in
// locales/schema/es.json, keyed by node type + field key. Add the entry when you add a field.
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import es from './schema/es.json'
import { NODE_CONFIG_FIELDS } from '../pages/nodeConfigFields.js'

const DIR = fileURLToPath(new URL('../../../../internal/workflow/schemas', import.meta.url))
const schemas = readdirSync(DIR)
  .filter(f => f.endsWith('.json') && !f.startsWith('._'))
  .map(f => [f.slice(0, -5), JSON.parse(readFileSync(join(DIR, f), 'utf8'))])

// [nodeType, fieldKey or "@handle", attribute, English text]
const fieldTexts = (type, f) => [
  ...['label', 'help', 'placeholder'].map(a => [type, f.key, a, f[a]]),
  [type, f.key, 'createLabel', f.resource?.create_label],
  ...(f.helpContent || []).map((s, i) => [type, f.key, `section${i}`, s.title]),
]
const withText = list => list.filter(([, , , en]) => /[A-Za-z]/.test(en || ''))
const bundled = withText(schemas.flatMap(([type, s]) => [
  ...(s.fields || []).flatMap(f => fieldTexts(type, f)),
  ...(s.sub_inputs || []).map(i => [type, `@${i.handle}`, 'label', i.label]),
]))
const fallback = withText(Object.entries(NODE_CONFIG_FIELDS).flatMap(([type, fields]) => fields.flatMap(f => fieldTexts(type, f))))

const missing = list => list.filter(([t, k, a]) => !es[t]?.[k]?.[a]?.trim()).map(([t, k, a]) => `${t} ${k} ${a}`)
const tokens = s => (String(s).match(/\{\{.*?\}\}/g) || []).sort()

describe('Spanish catalog of node schema texts', () => {
  it('reads the bundled schemas', () => {
    expect(schemas.length).toBeGreaterThan(150)
    expect(bundled.length).toBeGreaterThan(2000)
  })
  it('has an entry for every text of every bundled schema field and sub-node input', () => {
    expect(missing(bundled)).toEqual([])
  })
  it("has an entry for every text of the editor's fallback fields", () => {
    expect(missing(fallback)).toEqual([])
  })
  it('keeps the {{ }} expressions of the English text', () => {
    const seen = new Set()
    const bad = []
    for (const [t, k, a, en] of [...bundled, ...fallback]) {
      const id = `${t} ${k} ${a}`
      if (seen.has(id)) continue // the schema file's text wins over the fallback's
      seen.add(id)
      const spanish = es[t]?.[k]?.[a]
      if (spanish && JSON.stringify(tokens(en)) !== JSON.stringify(tokens(spanish))) bad.push(id)
    }
    expect(bad).toEqual([])
  })
})

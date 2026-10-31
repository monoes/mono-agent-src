import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

// Every GoApp.Name(...) a service calls must be exported by the generated bindings (App.js) and
// declared in App.d.ts. A binding that was hand-added to a service but not to the bindings makes
// the call fail at run time (and the build report IMPORT_IS_UNDEFINED), so this fails first.
const dir = new URL('.', import.meta.url).pathname
const bindings = readFileSync(join(dir, '../wailsjs/go/main/App.js'), 'utf8')
const types = readFileSync(join(dir, '../wailsjs/go/main/App.d.ts'), 'utf8')

const calls = []
for (const f of readdirSync(dir)) {
  if (!f.endsWith('.js') || f.endsWith('.test.js')) continue
  const src = readFileSync(join(dir, f), 'utf8')
  for (const m of src.matchAll(/\bGoApp\.([A-Za-z0-9_]+)\s*(?:\?\.)?\(/g)) calls.push([f, m[1]])
}

describe('the services only call bindings that exist', () => {
  it('finds the person item calls', () => {
    expect(calls.filter(([f]) => f === 'personItems.js').map(([, n]) => n)).toContain('PersonItemShow')
  })
  it.each(calls)('%s calls %s', (_f, name) => {
    expect(bindings).toMatch(new RegExp(`export function ${name}\\(`))
    expect(types).toMatch(new RegExp(`export function ${name}\\(`))
  })
})

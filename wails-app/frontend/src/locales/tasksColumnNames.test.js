// A Spanish tasks.* string that points at a board column names it the way
// the column header does, so "desde Inbox" never sends a person looking for
// a column labelled "Bandeja".
import { describe, it, expect } from 'vitest'
import en from './en.json'
import es from './es.json'

const flat = (o, p = '') => Object.entries(o).flatMap(([k, v]) => (v && typeof v === 'object' ? flat(v, `${p}${k}.`) : [[`${p}${k}`, v]]))
const word = (s) => new RegExp(`\\b${s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\b`)

describe('tasks column names in Spanish', () => {
  const enStrings = flat(en.tasks).filter(([k]) => !k.startsWith('column.')
    // A session's state names the session, not a board column: "Done" there is
    // "Completada", kept apart from the Done column's "Hecho" on purpose.
    && !k.startsWith('session.state.'))
  const esStrings = Object.fromEntries(flat(es.tasks))
  const pointers = enStrings.flatMap(([k, v]) => Object.keys(en.tasks.column)
    .filter(c => word(en.tasks.column[c]).test(v))
    .map(c => ({ key: k, column: c })))

  it('finds the strings that point at a column', () => {
    expect(pointers.map(p => p.key)).toContain('emptyBy.ready')
  })
  it('names each column with its Spanish header', () => {
    const off = pointers.filter(({ key, column }) => !String(esStrings[key] ?? '').includes(es.tasks.column[column]))
    expect(off).toEqual([])
  })
  it('leaves no English column name in the Spanish board copy', () => {
    const english = Object.entries(esStrings).filter(([, v]) => word('Inbox').test(v))
    expect(english).toEqual([])
  })
  it('keeps the Spanish session states apart from the column headers', () => {
    const headers = Object.values(es.tasks.column)
    const states = Object.values(es.tasks.session.state)
    expect(states.filter(v => headers.includes(v))).toEqual([])
    expect(new Set(states).size).toBe(states.length)
  })
})

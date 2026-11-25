import { describe, expect, it } from 'vitest'
import { applySurfaceEvent, applyActionEvent, getPointer, setPointer } from './a2uiModel.js'

const v = (m) => ({ version: '1.0', ...m })
const ev = (surfaceId, message, extra = {}) => ({ surfaceId, provenance: 'agent', message, ...extra })

describe('a2uiModel', () => {
  it('create then update components merge by id', () => {
    let s = applySurfaceEvent([], ev('a', v({ createSurface: { surfaceId: 'a' } })), 1)
    s = applySurfaceEvent(s, ev('a', v({ updateComponents: { surfaceId: 'a', components: [{ id: 'root', component: 'Text', text: 'x' }] } })), 2)
    s = applySurfaceEvent(s, ev('a', v({ updateComponents: { surfaceId: 'a', components: [{ id: 'root', component: 'Text', text: 'y' }, { id: 'b', component: 'Divider' }] } })), 3)
    expect(s).toHaveLength(1)
    expect(s[0].components.root.text).toBe('y')
    expect(Object.keys(s[0].components)).toEqual(['root', 'b'])
  })
  it('ignores replayed seqs', () => {
    const a = applySurfaceEvent([], ev('a', v({ createSurface: { surfaceId: 'a', dataModel: { n: 1 } } })), 5)
    const b = applySurfaceEvent(a, ev('a', v({ updateDataModel: { surfaceId: 'a', path: '/n', value: 2 } })), 5)
    expect(b).toBe(a)
    const c = applySurfaceEvent(a, ev('a', v({ updateDataModel: { surfaceId: 'a', path: '/n', value: 2 } })), 6)
    expect(c[0].dataModel.n).toBe(2)
  })
  it('data model pointer set, null deletes, root replaces', () => {
    let s = applySurfaceEvent([], ev('a', v({ createSurface: { surfaceId: 'a', dataModel: { x: { y: 1 }, z: 2 } } })), 1)
    s = applySurfaceEvent(s, ev('a', v({ updateDataModel: { surfaceId: 'a', path: '/x/y', value: 9 } })), 2)
    s = applySurfaceEvent(s, ev('a', v({ updateDataModel: { surfaceId: 'a', path: '/z', value: null } })), 3)
    expect(s[0].dataModel).toEqual({ x: { y: 9 } })
    s = applySurfaceEvent(s, ev('a', v({ updateDataModel: { surfaceId: 'a', value: { q: 1 } } })), 4)
    expect(s[0].dataModel).toEqual({ q: 1 })
  })
  it('rejects prototype pollution paths', () => {
    expect(setPointer({}, '/__proto__/polluted', 1)).toEqual({})
    expect({}.polluted).toBeUndefined()
    expect(getPointer({ a: 1 }, '/constructor')).toBeUndefined()
  })
  it('deleteSurface removes it; unknown surface updates are ignored', () => {
    let s = applySurfaceEvent([], ev('a', v({ createSurface: { surfaceId: 'a' } })), 1)
    s = applySurfaceEvent(s, ev('a', v({ deleteSurface: { surfaceId: 'a' } })), 2)
    expect(s).toEqual([])
    expect(applySurfaceEvent([], ev('zz', v({ updateDataModel: { surfaceId: 'zz', value: {} } })), 1)).toEqual([])
  })
  it('keeps provenance and ref from creation, records partIndex', () => {
    const s = applySurfaceEvent([], { surfaceId: 'a', provenance: 'system', ref: { kind: 'hil', id: '1' }, message: v({ createSurface: { surfaceId: 'a' } }) }, 1, 3)
    expect(s[0]).toMatchObject({ provenance: 'system', ref: { kind: 'hil', id: '1' }, partIndex: 3 })
  })
  it('tolerates garbage messages and keeps unknown component types', () => {
    expect(applySurfaceEvent([], ev('a', null), 1)).toEqual([])
    const s = applySurfaceEvent([], ev('a', v({ createSurface: { surfaceId: 'a', components: [{ id: 'root', component: 'Hologram' }, 7, { nope: 1 }] } })), 1)
    expect(s[0].components.root.component).toBe('Hologram')
  })
  it('a press never settles a system surface; the CLI replaces it itself', () => {
    const s = applySurfaceEvent([], ev('a', v({ createSurface: { surfaceId: 'a' } }), { provenance: 'system' }), 1)
    expect(applyActionEvent(s, { surfaceId: 'a', outcome: 'accepted' })).toBe(s)
  })
  it('ui.action outcome marks status', () => {
    const s = applySurfaceEvent([], ev('a', v({ createSurface: { surfaceId: 'a' } })), 1)
    expect(applyActionEvent(s, { surfaceId: 'a', outcome: 'accepted' })[0].status).toBe('resolved')
    expect(applyActionEvent(s, { surfaceId: 'a', outcome: 'rejected', reason: 'no' })[0]).toMatchObject({ status: 'live', lastReason: 'no' })
    expect(applyActionEvent(s, { surfaceId: 'q', outcome: 'accepted' })).toBe(s)
  })
})

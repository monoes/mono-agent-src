// @vitest-environment jsdom
// Work bounds on hostile reference graphs. A runaway synchronous walk cannot be
// stopped by the test runner's timeout, so every read of a child list goes
// through a meter that throws past a read count or a deadline: remove a bound in
// planTree (or add an unbounded walk) and these fail at once instead of hanging.
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import '../../i18n.js'
import { A2UISurface, MAX_NODES, planTree } from './A2UISurface.jsx'
import { applySurfaceEvent, setPointer, MAX_SURFACE_COMPONENTS } from '../../lib/a2uiModel.js'

const DEADLINE_MS = 3000
const meter = maxReads => {
  const start = performance.now()
  const m = { reads: 0 }
  m.tick = () => {
    m.reads += 1
    if (m.reads > maxReads || performance.now() - start > DEADLINE_MS) throw new Error(`work bound exceeded after ${m.reads} reads`)
  }
  return m
}
// A child list whose every element read is metered.
const metered = (list, m) => new Proxy(list, { get: (t, k) => { if (typeof k === 'string' && /^\d+$/.test(k)) m.tick(); return Reflect.get(t, k) } })

// c0..c8, each listing the next one 40 times: 40^8 paths if every reference were followed.
const fanOut = m => {
  const ids = ['root', 'c1', 'c2', 'c3', 'c4', 'c5', 'c6', 'c7', 'c8']
  return Object.fromEntries(ids.map((id, i) => [id, i === 8
    ? { id, component: 'Text', text: 'leaf' }
    : { id, component: 'Column', children: metered(Array(40).fill(ids[i + 1]), m) }]))
}
const surface = components => ({ surfaceId: 's', provenance: 'agent', components, dataModel: {}, status: 'live', lastSeq: 1 })

let errors
beforeEach(() => { errors = vi.spyOn(console, 'error') })
afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe('A2UI work bounds', () => {
  it('plans a 40-way repeated reference graph in at most MAX_NODES visits, each component once', () => {
    const m = meter(MAX_NODES)
    const plan = planTree(fanOut(m))
    expect(plan.visits).toBeLessThanOrEqual(MAX_NODES)
    expect(m.reads).toBeLessThan(MAX_NODES)
    expect(plan.order).toEqual(['root', 'c1', 'c2', 'c3', 'c4', 'c5', 'c6', 'c7', 'c8'])
    expect(plan.truncated).toBe(true)
  })

  it('draws that graph once per component, with a title, in bounded work', () => {
    // Drawing, the title, the announcement and re-renders never walk the lists again.
    const m = meter(2 * MAX_NODES)
    const s = surface(fanOut(m))
    const started = performance.now()
    const { container, rerender } = render(<A2UISurface surface={s} />)
    rerender(<A2UISurface surface={{ ...s, dataModel: { tick: 1 }, lastSeq: 2 }} />)
    expect(performance.now() - started).toBeLessThan(DEADLINE_MS)
    expect(container.querySelectorAll('.a2ui-column')).toHaveLength(8)
    expect(screen.getAllByText('leaf')).toHaveLength(1)
    expect(screen.getByRole('region', { name: 'from agent: leaf' })).toBeTruthy()
    expect(errors).not.toHaveBeenCalled()
  })

  it('charges unknown, missing and non-string references to the same budget', () => {
    for (const ref of ['nope', 'toString', '__proto__', 7, null]) {
      const m = meter(MAX_NODES)
      const plan = planTree({ root: { id: 'root', component: 'Column', children: metered(Array(1_000_000).fill(ref), m) } })
      expect(plan.visits).toBe(MAX_NODES)
      expect(plan.truncated).toBe(true)
      expect(plan.order).toEqual(['root'])
    }
    // Spread over many parents, the budget is shared, not per list.
    const m = meter(MAX_NODES)
    const parents = Array.from({ length: 50 }, (_, i) => `p${i}`)
    const map = { root: { component: 'Column', children: metered(parents, m) } }
    for (const p of parents) map[p] = { component: 'Row', children: metered(Array(1000).fill('gone'), m) }
    expect(planTree(map).visits).toBe(MAX_NODES)
  })

  it('draws a repeated child of a Card or Button at most once', () => {
    const s = surface({
      root: { id: 'root', component: 'Column', children: ['t', 't', 'card', 'card'] },
      t: { id: 't', component: 'Text', text: 'once' },
      card: { id: 'card', component: 'Card', child: 't' },
    })
    const { container } = render(<A2UISurface surface={s} />)
    expect(screen.getAllByText('once')).toHaveLength(1)
    expect(container.querySelectorAll('.a2ui-card')).toHaveLength(1)
    expect(errors).not.toHaveBeenCalled()
  })

  it('bounds the model fold: components read and kept, and pointer depth', () => {
    const m = meter(MAX_SURFACE_COMPONENTS)
    const many = Array.from({ length: 100_000 }, (_, i) => ({ id: i ? `n${i}` : 'root', component: 'Divider' }))
    const msg = body => ({ surfaceId: 's', message: { version: '1.0', ...body } })
    let list = applySurfaceEvent([], msg({ createSurface: { surfaceId: 's', components: metered(many, m) } }), 1)
    expect(Object.keys(list[0].components)).toHaveLength(MAX_SURFACE_COMPONENTS)
    list = applySurfaceEvent(list, msg({ updateComponents: { surfaceId: 's', components: [{ id: 'extra', component: 'Divider' }, { id: 'root', component: 'Column', children: [] }] } }), 2)
    expect(list[0].components.extra).toBeUndefined()
    expect(list[0].components.root.component).toBe('Column')
    const deep = '/a'.repeat(200_000)
    expect(setPointer({ k: 1 }, deep, 1)).toEqual({ k: 1 })
  })
})

import { describe, it, expect } from 'vitest'
import {
  deriveOutputs, deriveInputs, caseHandles, portsDependOnConfig,
  isCaseListSettled, remapSourceEdges, NODE_CONFIG_FIELDS,
  arrayFieldItems, arrayTagLabel, normalizeConfigForSave, portRenames,
  canonicalOutputHandle, loopBackEdges, hasForbiddenCycle, loopEdgePath, loopEdgeBottom,
  loopEdgeTop, chooseLoopEdgeY, edgePoints,
  makeIdGen,
} from './nodeConfigFields.js'

const ids = ports => ports.map(p => p.id)

describe('deriveOutputs', () => {
  it('core.switch follows its cases then the default handle (switch.go)', () => {
    expect(ids(deriveOutputs('core.switch', {
      cases: ['active', { value: 'x', handle: 'custom' }, { value: 'nohandle' }, 'active'],
    }))).toEqual(['active', 'custom', 'default'])
    expect(ids(deriveOutputs('core.switch', { cases: ['a'], default_handle: 'rest' }))).toEqual(['a', 'rest'])
    // default handle not duplicated when a case already uses it
    expect(ids(deriveOutputs('core.switch', { cases: ['default', 'b'] }))).toEqual(['default', 'b'])
    // JSON-array string (textarea fallback editor)
    expect(ids(deriveOutputs('core.switch', { cases: '[{"value":"active","handle":"active"},"idle"]' })))
      .toEqual(['active', 'idle', 'default'])
    expect(ids(deriveOutputs('core.switch'))).toEqual(['default'])
    expect(ids(deriveOutputs('core.switch', null))).toEqual(['default'])
  })

  it('core.filter emits main/rejected like filter.go', () => {
    expect(ids(deriveOutputs('core.filter', {}))).toEqual(['main', 'rejected'])
  })

  it('ai.choose: case handles (object handle falls back to value) + low_confidence', () => {
    expect(ids(deriveOutputs('ai.choose', {
      cases: ['billing', { value: 'tech', handle: 'support' }, { value: 'other' }, '{"value":"spam","handle":"junk"}'],
    }))).toEqual(['billing', 'support', 'other', 'junk', 'low_confidence'])
    expect(ids(deriveOutputs('ai.choose', { cases: '' }))).toEqual(['low_confidence'])
    expect(ids(deriveOutputs('ai.choose', { cases: ['a', 'low_confidence'] }))).toEqual(['a', 'low_confidence'])
  })

  it('keeps the fixed ports of other types', () => {
    expect(ids(deriveOutputs('core.if', {}))).toEqual(['true', 'false'])
    expect(ids(deriveOutputs('http.request'))).toEqual(['out', 'error'])
    expect(ids(deriveOutputs('agent.ask', { cases: ['x'] }))).toEqual(['main'])
    expect(deriveOutputs('core.stop_error')).toEqual([])
    expect(deriveInputs('trigger.manual')).toEqual([])
    expect(ids(deriveInputs('ai.choose'))).toEqual(['in'])
  })
})

describe('port re-derivation helpers', () => {
  it('portsDependOnConfig', () => {
    expect(portsDependOnConfig('core.switch', 'cases')).toBe(true)
    expect(portsDependOnConfig('core.switch', 'default_handle')).toBe(true)
    expect(portsDependOnConfig('ai.choose', 'cases')).toBe(true)
    expect(portsDependOnConfig('ai.choose', 'input')).toBe(false)
    expect(portsDependOnConfig('core.filter', 'condition')).toBe(false)
  })

  it('isCaseListSettled holds ports while JSON is mid-edit', () => {
    expect(isCaseListSettled('[{"value":')).toBe(false)
    expect(isCaseListSettled('["a"]')).toBe(true)
    expect(isCaseListSettled(['a'])).toBe(true)
    expect(isCaseListSettled('a, b')).toBe(true)
  })

  it('remapSourceEdges re-points by handle and drops vanished handles', () => {
    const edges = [
      { id: 'e1', source: 'n', sourcePortId: 'b', sourcePortIdx: 1, target: 't' },
      { id: 'e2', source: 'n', sourcePortId: 'gone', sourcePortIdx: 2, target: 't' },
      { id: 'e3', source: 'other', sourcePortId: 'main', sourcePortIdx: 0, target: 'n' },
      { id: 'e4', source: 'n', sourcePortId: 'low_confidence', sourcePortIdx: 3, target: 't' },
    ]
    const outputs = deriveOutputs('ai.choose', { cases: ['b'] })
    const got = remapSourceEdges(edges, 'n', outputs)
    expect(got.map(e => [e.id, e.sourcePortIdx])).toEqual([['e1', 0], ['e3', 0], ['e4', 1]])
    expect(got[1]).toBe(edges[2])
  })

  it('caseHandles dedupes and skips blanks', () => {
    expect(caseHandles(['a', ' ', 'a', { handle: 'b' }])).toEqual(['a', 'b'])
    expect(caseHandles('a, b ,c')).toEqual(['a', 'b', 'c'])
  })
})

describe('config field fallbacks', () => {
  it('core.switch uses the backend keys and handled cases', () => {
    const f = NODE_CONFIG_FIELDS['core.switch']
    expect(f.map(x => x.key)).toContain('field')
    expect(f.map(x => x.key)).not.toContain('expression')
    const cases = JSON.parse(f.find(x => x.key === 'cases').default)
    expect(cases.every(c => c.handle)).toBe(true)
  })

  it('ai.choose has its fields', () => {
    const keys = NODE_CONFIG_FIELDS['ai.choose'].map(x => x.key)
    for (const k of ['cases', 'input', 'fields', 'instructions', 'extra_questions', 'min_confidence', 'output_key', 'api_key', 'model', 'concurrency']) {
      expect(keys).toContain(k)
    }
    expect(NODE_CONFIG_FIELDS['ai.choose'].find(x => x.key === 'api_key').default).toBe('@secret:typesafe')
  })
})

// Regressions found driving the editor in a browser (2026-09-25).
describe('array field editor values', () => {
  it('object cases render as text, not as a React child (inspector crashed)', () => {
    const items = arrayFieldItems([{ value: 'tech', handle: 'support', description: 'tech issue' }, 'billing'])
    expect(items.map(arrayTagLabel)).toEqual(['{"value":"tech","handle":"support","description":"tech issue"}', 'billing'])
    expect(items.map(arrayTagLabel).every(l => typeof l === 'string')).toBe(true)
    expect(arrayTagLabel(3)).toBe('3')
    expect(arrayTagLabel(null)).toBe('')
  })

  it('a JSON-array string is parsed, not comma-split into fragments', () => {
    expect(arrayFieldItems('[{"value":"active","handle":"active"},"idle"]'))
      .toEqual([{ value: 'active', handle: 'active' }, 'idle'])
    expect(arrayFieldItems('a, b')).toEqual(['a', 'b'])
    expect(arrayFieldItems('[not json, x')).toEqual(['[not json', 'x'])
    expect(arrayFieldItems('')).toEqual([])
    expect(arrayFieldItems(undefined)).toEqual([])
  })
})

describe('normalizeConfigForSave', () => {
  it('core.switch JSON-string cases are saved as a list (switch.go ignores strings)', () => {
    const cfg = { field: '{{ $json.s }}', cases: '[{"value":"a","handle":"a"},"b"]' }
    expect(normalizeConfigForSave('core.switch', cfg)).toEqual({ field: '{{ $json.s }}', cases: [{ value: 'a', handle: 'a' }, 'b'] })
    expect(cfg.cases).toBe('[{"value":"a","handle":"a"},"b"]') // input not mutated
  })

  it('leaves mid-edit text, arrays and other node types alone', () => {
    const mid = { cases: '[{"value":' }
    expect(normalizeConfigForSave('core.switch', mid)).toBe(mid)
    const arr = { cases: ['a'] }
    expect(normalizeConfigForSave('ai.choose', arr)).toBe(arr)
    const other = { cases: '["a"]' }
    expect(normalizeConfigForSave('core.set', other)).toBe(other)
    expect(normalizeConfigForSave('core.switch', null)).toEqual({})
  })
})

describe('renaming a handle in place keeps its edges', () => {
  const edges = [
    { id: 'e1', source: 'sw', sourcePortId: 'active', sourcePortIdx: 0, target: 'a' },
    { id: 'e2', source: 'sw', sourcePortId: 'default', sourcePortIdx: 2, target: 'b' },
  ]
  it('typing a new core.switch default_handle re-points the default edge (was dropped on the first key)', () => {
    let prev = deriveOutputs('core.switch', { cases: ['active', 'idle'] })
    let cur = edges
    for (const typed of ['o', 'ot', 'oth', 'othe', 'other']) {
      const next = deriveOutputs('core.switch', { cases: ['active', 'idle'], default_handle: typed })
      cur = remapSourceEdges(cur, 'sw', next, prev)
      prev = next
    }
    expect(cur.map(e => [e.id, e.sourcePortId, e.sourcePortIdx])).toEqual([['e1', 'active', 0], ['e2', 'other', 2]])
  })

  it('editing one case handle in the JSON editor follows the rename', () => {
    const prev = deriveOutputs('core.switch', { cases: '[{"value":"a","handle":"active"},"idle"]' })
    const next = deriveOutputs('core.switch', { cases: '[{"value":"a","handle":"activ"},"idle"]' })
    expect(portRenames(prev, next)).toEqual({ active: 'activ' })
    expect(remapSourceEdges(edges, 'sw', next, prev).map(e => e.sourcePortId)).toEqual(['activ', 'default'])
  })

  it('removing or reordering is not a rename', () => {
    const p = deriveOutputs('ai.choose', { cases: ['billing', 'support', 'sales'] })
    expect(portRenames(p, deriveOutputs('ai.choose', { cases: ['billing', 'sales'] }))).toEqual({})
    expect(portRenames(p, deriveOutputs('ai.choose', { cases: ['support', 'billing', 'sales'] }))).toEqual({})
    expect(portRenames(p, deriveOutputs('ai.choose', { cases: ['billing', 'sales', 'sales2'] }))).toEqual({})
    expect(portRenames(null, p)).toEqual({})
  })
})

describe('loops (core.split_in_batches)', () => {
  const n = (id, subtype) => ({ id, subtype, outputs: deriveOutputs(subtype, {}) })
  const e = (source, sourcePortId, target) => ({ source, sourcePortId, target })
  const nodes = [n('t', 'trigger.manual'), n('L', 'core.split_in_batches'), n('a', 'http.request'), n('b', 'core.set'), n('d', 'core.set')]

  it('has loop and done ports; legacy "batch" edges map to loop', () => {
    expect(ids(deriveOutputs('core.split_in_batches', {}))).toEqual(['loop', 'done'])
    expect(canonicalOutputHandle('core.split_in_batches', 'batch')).toBe('loop')
    expect(canonicalOutputHandle('core.split_in_batches', 'done')).toBe('done')
    expect(canonicalOutputHandle('core.if', 'batch')).toBe('batch')
  })

  it('human_in_loop offers the opt-in rejected port', () => {
    expect(ids(deriveOutputs('core.human_in_loop', {}))).toEqual(['main', 'rejected'])
  })

  it('allows a back-edge from the loop body into the loop node', () => {
    const edges = [e('t', 'main', 'L'), e('L', 'loop', 'a'), e('a', 'out', 'b'), e('b', 'main', 'L'), e('L', 'done', 'd')]
    expect(hasForbiddenCycle(nodes, edges)).toBe(false)
    const back = loopBackEdges(nodes, edges)
    expect([...back.keys()]).toEqual([edges[3]])
    expect([...back.get(edges[3])].sort()).toEqual(['L', 'a', 'b'])
    // drawn in the other order: the closing edge is the loop output itself
    const reordered = [e('a', 'out', 'b'), e('b', 'main', 'L'), e('L', 'loop', 'a')]
    expect(hasForbiddenCycle(nodes, reordered)).toBe(false)
    // legacy handle
    expect(hasForbiddenCycle(nodes, [e('L', 'batch', 'a'), e('a', 'out', 'L')])).toBe(false)
  })

  it('rejects cycles that do not close into a loop node', () => {
    expect(hasForbiddenCycle(nodes, [e('a', 'out', 'b'), e('b', 'main', 'a')])).toBe(true)
    // inside a loop body, a cycle that skips the loop node
    expect(hasForbiddenCycle(nodes, [e('L', 'loop', 'a'), e('a', 'out', 'b'), e('b', 'main', 'a')])).toBe(true)
    // from the done branch back into the loop node is not a loop
    expect(hasForbiddenCycle(nodes, [e('L', 'done', 'd'), e('d', 'main', 'L')])).toBe(true)
  })
})

describe('loopEdgePath', () => {
  const points = d => [...d.matchAll(/(-?\d+(?:\.\d+)?),(-?\d+(?:\.\d+)?)/g)].map(m => [+m[1], +m[2]])

  it('leaves the source rightwards, runs along the bottom and enters the target from the left', () => {
    const d = loopEdgePath(700, 100, 300, 100, 260, 24, 10)
    expect(d.startsWith('M700,100 H714')).toBe(true)
    expect(d).toContain('V250')
    expect(d).toContain('H286')        // bottom run back towards the target
    expect(d.endsWith('H300')).toBe(true)
    for (const [, y] of points(d)) expect(y).toBeLessThanOrEqual(260)
    expect(points(d).some(([, y]) => y === 260)).toBe(true)
  })

  it('stays well-formed when the target sits right of the source', () => {
    const d = loopEdgePath(300, 100, 700, 120, 260, 24, 10)
    expect(d).toContain('Q324,260 334,260')
    expect(d.endsWith('H700')).toBe(true)
  })
})

describe('loopEdgeBottom', () => {
  // The r2 auto-layout: Split → Set → (back to Split), with the done branch's
  // Remove Duplicates right under Set.
  const boxes = [
    { id: 'L', x: 780, y: 184, w: 220, h: 114 },
    { id: 'set', x: 1040, y: 184, w: 220, h: 86 },
    { id: 'rd', x: 1040, y: 323, w: 220, h: 86 },
    { id: 'far', x: 2000, y: 323, w: 220, h: 86 },
  ]
  const loop = new Set(['L', 'set'])

  it('runs below the loop nodes and below any node the run crosses', () => {
    expect(loopEdgeBottom(boxes, loop, 1272, 768)).toBe(323 + 86 + 28)
  })

  it('ignores nodes outside the run and keeps just below the loop when clear', () => {
    const clear = boxes.filter(b => b.id !== 'rd')
    expect(loopEdgeBottom(clear, loop, 1272, 768)).toBe(298 + 28)
  })

  it('keeps pushing down past stacked nodes', () => {
    const stacked = [...boxes, { id: 'rd2', x: 900, y: 430, w: 220, h: 86 }]
    expect(loopEdgeBottom(stacked, loop, 1272, 768)).toBe(430 + 86 + 28)
  })
})

describe('makeIdGen', () => {
  it('moves past loaded ids and skips taken ones', () => {
    const ids = makeIdGen('nr')
    expect(ids.next()).toBe('nr1')
    ids.advancePast(['nr1', 'nr5', 'n7', 'nrx', 'abc'])
    expect(ids.next()).toBe('nr6')
    expect(ids.next(new Set(['nr7', 'nr8']))).toBe('nr9')
    ids.advancePast(['nr3'])          // never moves backwards
    expect(ids.next()).toBe('nr10')
  })
})

describe('chooseLoopEdgeY', () => {
  // r3 auto-layout: Trigger → Split → Set → Wait in one row, Split's done
  // branch Remove Duplicates under Set, also feeding Wait; Set loops back.
  const boxes = [
    { id: 't', x: 60, y: 60, w: 220, h: 86 },
    { id: 'L', x: 320, y: 60, w: 220, h: 114 },
    { id: 'set', x: 580, y: 60, w: 220, h: 86 },
    { id: 'rd', x: 580, y: 200, w: 220, h: 86 },
    { id: 'wait', x: 840, y: 60, w: 220, h: 86 },
  ]
  const loopIds = new Set(['L', 'set'])
  const back = { sx: 800, sy: 126, tx: 320, ty: 126 }
  const curves = [
    [280, 126, 320, 126], // trigger → split
    [540, 126, 580, 126], // split loop → set
    [540, 154, 580, 266], // split done → remove duplicates
    [800, 126, 840, 126], // set → wait
    [800, 266, 840, 126], // remove duplicates → wait
  ]
  const obstacles = [
    ...curves.flatMap(([a, b, c, d]) => edgePoints(a, b, c, d)),
    [280, 126], [800, 266], // output ports of trigger and remove duplicates
  ]

  it('goes above the row when the route below would hug the done branch', () => {
    expect(loopEdgeBottom(boxes, loopIds, 812, 308)).toBe(314)
    expect(loopEdgeTop(boxes, loopIds, 812, 308)).toBe(32)
    expect(chooseLoopEdgeY({ ...back, boxes, loopIds, obstacles })).toBe(32)
  })

  it('stays below on a tie', () => {
    expect(chooseLoopEdgeY({ ...back, boxes, loopIds, obstacles: [] })).toBe(314)
  })

  it('clears a node sitting above the row', () => {
    const top = [...boxes, { id: 'note', x: 400, y: -60, w: 220, h: 86 }]
    expect(loopEdgeTop(top, loopIds, 812, 308)).toBe(-60 - 28)
  })

  it('draws the route above as a rise, run and drop', () => {
    const d = loopEdgePath(800, 126, 320, 126, 32)
    expect(d).toContain('Q812,126 812,118 V40 Q812,32 804,32')
    expect(d).toContain('H316 Q308,32 308,40 V118 Q308,126 316,126 H320')
  })
})

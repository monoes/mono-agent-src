import { describe, it, expect } from 'vitest'
import { categoryOf, workflowToCanvas } from './workflowCanvas.js'

const deps = {
  normalizeNodeType: t => t,
  catColor: c => `c-${c}`,
  getConfigFields: () => ['f'],
  resolvePortIdx: (ports, h) => Math.max(0, (ports || []).findIndex(p => p.id === h)),
  deriveInputs: () => [{ id: 'main' }],
  deriveOutputs: () => [{ id: 'true' }, { id: 'false' }],
  canonicalOutputHandle: (_t, h) => h,
}

describe('workflowToCanvas', () => {
  it('maps prefixes to palette categories and keeps unknown prefixes', () => {
    expect(categoryOf('core.if')).toBe('control')
    expect(categoryOf('db.query')).toBe('database')
    expect(categoryOf('foo.bar')).toBe('foo')
  })
  it('maps nodes and resolves edge handles to port indexes', () => {
    const { nodes, edges } = workflowToCanvas({
      nodes: [{ id: 'a', name: 'A', node_type: 'core.if', position_x: 1, position_y: 2 }, { id: 'b', type: 'http.request' }],
      connections: [{ id: 'e', source_node_id: 'a', source_handle: 'false', target_node_id: 'b', target_handle: 'main' }],
    }, deps)
    expect(nodes[0]).toMatchObject({ id: 'a', category: 'control', color: 'c-control', x: 1, y: 2, runStatus: null })
    expect(nodes[1].category).toBe('http')
    expect(edges[0]).toMatchObject({ source: 'a', sourcePortIdx: 1, targetPortIdx: 0 })
  })
})

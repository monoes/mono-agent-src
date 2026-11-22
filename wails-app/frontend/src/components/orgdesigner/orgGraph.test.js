import { describe, it, expect } from 'vitest'
import {
  validateStructure,
  wouldCycle,
  tidyTree,
  hydrate,
  dehydrate,
  reportPath,
  elbowPath,
  NODE_W,
} from './orgGraph.js'

describe('validateStructure', () => {
  it('passes for a single root', () => {
    const nodes = [
      { id: 'a', parentId: null },
      { id: 'b', parentId: 'a' },
    ]
    expect(validateStructure(nodes)).toEqual({ valid: true, errors: [] })
  })

  it('does not report a missing root for an empty graph (a Live view before any run)', () => {
    expect(validateStructure([])).toEqual({ valid: true, errors: [] })
  })

  it('fails with the right message when there is no root', () => {
    const nodes = [
      { id: 'a', parentId: 'b' },
      { id: 'b', parentId: 'a' },
    ]
    const { valid, errors } = validateStructure(nodes)
    expect(valid).toBe(false)
    expect(errors).toContain('no root role — exactly one role must have reports_to: null')
  })

  it('fails with the right message (including both ids) for multiple roots', () => {
    const nodes = [
      { id: 'a', parentId: null },
      { id: 'b', parentId: null },
    ]
    const { valid, errors } = validateStructure(nodes)
    expect(valid).toBe(false)
    const msg = errors.find(e => e.startsWith('multiple root roles'))
    expect(msg).toBeDefined()
    expect(msg).toContain('a')
    expect(msg).toContain('b')
  })

  it('fails on duplicate ids', () => {
    const nodes = [
      { id: 'a', parentId: null },
      { id: 'a', parentId: null },
    ]
    const { valid, errors } = validateStructure(nodes)
    expect(valid).toBe(false)
    expect(errors).toContain('duplicate role id(s): a')
  })

  it('fails on self-report', () => {
    const nodes = [
      { id: 'a', parentId: null },
      { id: 'b', parentId: 'b' },
    ]
    const { valid, errors } = validateStructure(nodes)
    expect(valid).toBe(false)
    expect(errors).toContain('role "b" reports to itself')
  })

  it('catches a cycle among non-root roles even with a separate valid root', () => {
    const nodes = [
      { id: 'a', parentId: null },
      { id: 'b', parentId: 'c' },
      { id: 'c', parentId: 'b' },
    ]
    const { valid, errors } = validateStructure(nodes)
    expect(valid).toBe(false)
    expect(errors).toContain('circular reporting: b -> c -> b')
  })
})

describe('wouldCycle', () => {
  it('is true when reassigning an ancestor to report to its own descendant', () => {
    const nodes = [
      { id: 'a', parentId: null },
      { id: 'b', parentId: 'a' },
      { id: 'c', parentId: 'b' },
    ]
    // a -> c would make a report to its own descendant c
    expect(wouldCycle(nodes, 'a', 'c')).toBe(true)
  })

  it('is false for an unrelated valid reassignment', () => {
    const nodes = [
      { id: 'a', parentId: null },
      { id: 'b', parentId: 'a' },
      { id: 'c', parentId: 'a' },
    ]
    expect(wouldCycle(nodes, 'c', 'b')).toBe(false)
  })

  it('is true for self', () => {
    const nodes = [
      { id: 'a', parentId: null },
      { id: 'b', parentId: 'a' },
    ]
    expect(wouldCycle(nodes, 'b', 'b')).toBe(true)
  })
})

describe('tidyTree', () => {
  it('centers a root over its two children, without overlap', () => {
    const nodes = [
      { id: 'root', parentId: null },
      { id: 'c1', parentId: 'root' },
      { id: 'c2', parentId: 'root' },
    ]
    const laid = tidyTree(nodes)
    const byId = Object.fromEntries(laid.map(n => [n.id, n]))
    expect(byId.root.x).toBeCloseTo((byId.c1.x + byId.c2.x) / 2)
    expect(Math.abs(byId.c1.x - byId.c2.x)).toBeGreaterThanOrEqual(NODE_W + 36)
  })

  it('gives monotonically increasing y per depth level across a 3-level tree', () => {
    const nodes = [
      { id: 'root', parentId: null },
      { id: 'mid', parentId: 'root' },
      { id: 'leaf', parentId: 'mid' },
    ]
    const laid = tidyTree(nodes)
    const byId = Object.fromEntries(laid.map(n => [n.id, n]))
    expect(byId.root.y).toBeLessThan(byId.mid.y)
    expect(byId.mid.y).toBeLessThan(byId.leaf.y)
  })

  it('does not mutate the input array', () => {
    const nodes = [
      { id: 'root', parentId: null, x: 0, y: 0 },
      { id: 'c1', parentId: 'root', x: 0, y: 0 },
    ]
    const snapshot = JSON.parse(JSON.stringify(nodes))
    tidyTree(nodes)
    expect(nodes).toEqual(snapshot)
  })
})

describe('hydrate / dehydrate round trip', () => {
  it('preserves unmodeled fields byte-for-byte', () => {
    const role = {
      id: 'a',
      title: 'A',
      type: 'boss',
      reports_to: null,
      responsibilities: ['x'],
      policy: { git: 'read' },
      adapter_config: { model: 'foo' },
      budget_usd: 5,
    }
    const [node] = hydrate([role])
    const [back] = dehydrate([node])
    expect(back.policy).toEqual({ git: 'read' })
    expect(back.adapter_config).toEqual({ model: 'foo' })
    expect(back.budget_usd).toBe(5)
    expect(back.id).toBe('a')
    expect(back.title).toBe('A')
    expect(back.type).toBe('boss')
    expect(back.reports_to).toBeNull()
    expect(back.responsibilities).toEqual(['x'])
  })

  it('leaves x/y undefined for a role with no ui position', () => {
    const [node] = hydrate([{ id: 'a', title: 'A', type: 'boss', reports_to: null, responsibilities: [] }])
    expect(node.x).toBeUndefined()
    expect(node.y).toBeUndefined()
  })
})

describe('reportPath / elbowPath', () => {
  it('return non-empty path strings starting with M and do not throw', () => {
    expect(reportPath(10, 100, 50, 0)).toMatch(/^M/)
    expect(elbowPath(10, 100, 50, 0)).toMatch(/^M/)
    expect(() => reportPath(0, 0, 0, 0)).not.toThrow()
    expect(() => elbowPath(0, 0, 0, 0)).not.toThrow()
    expect(reportPath(0, 0, 0, 0).length).toBeGreaterThan(0)
    expect(elbowPath(0, 0, 0, 0).length).toBeGreaterThan(0)
  })
})

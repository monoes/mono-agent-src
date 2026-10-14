import { describe, it, expect } from 'vitest'
import {
  aiPortsOf, withAiPorts, isAiEdge, checkSubConnection, subEdge, missingRequired,
  freeSubInput, attachedCount, subChildren, autoLayout, aiEdgePath,
} from './aiSubNodes.js'
import { hasForbiddenCycle, loopBackEdges } from './nodeConfigFields.js'

// Synthetic schemas to the contract (status-ux.md): no real node uses it yet.
const AGENT_SCHEMA = {
  fields: [],
  sub_inputs: [
    { handle: 'ai_languageModel', label: 'Chat Model', max: 1, required: true },
    { handle: 'ai_memory', label: 'Memory', max: 1 },
    { handle: 'ai_tool', label: 'Tool' },
  ],
}
const sub = kind => ({ fields: [], sub_output: kind })
const mainNode = (id, extra = {}) => ({ id, label: id, x: 0, y: 0, inputs: [{ id: 'in' }], outputs: [{ id: 'main' }], ...extra })
const agent = (id = 'agent') => withAiPorts(mainNode(id, { label: 'Agent' }), AGENT_SCHEMA)
const subNode = (id, kind, label = id) => withAiPorts(mainNode(id, { label }), sub(kind))

describe('aiPortsOf / withAiPorts', () => {
  it('reads sub_inputs and sub_output, defaulting max and required', () => {
    expect(aiPortsOf(AGENT_SCHEMA).subInputs).toEqual([
      { handle: 'ai_languageModel', label: 'Chat Model', max: 1, required: true },
      { handle: 'ai_memory', label: 'Memory', max: 1, required: false },
      { handle: 'ai_tool', label: 'Tool', max: 0, required: false },
    ])
    expect(aiPortsOf(sub('ai_tool'))).toEqual({ subInputs: [], subOutput: 'ai_tool' })
    expect(aiPortsOf(null)).toEqual({ subInputs: [], subOutput: '' })
    // Only ai_* kinds count.
    expect(aiPortsOf({ sub_inputs: [{ handle: 'main', label: 'x' }], sub_output: 'main' })).toEqual({ subInputs: [], subOutput: '' })
  })

  it('a root keeps its main ports and gains sub-inputs', () => {
    const a = agent()
    expect(a.inputs).toHaveLength(1)
    expect(a.outputs).toHaveLength(1)
    expect(a.subInputs.map(s => s.handle)).toEqual(['ai_languageModel', 'ai_memory', 'ai_tool'])
  })

  it('a sub-node loses its main ports (compact)', () => {
    const m = subNode('m', 'ai_languageModel')
    expect(m.inputs).toEqual([])
    expect(m.outputs).toEqual([])
    expect(m.subOutput).toBe('ai_languageModel')
  })

  it('returns the same node when nothing changes, so the canvas effect settles', () => {
    const a = agent()
    expect(withAiPorts(a, AGENT_SCHEMA)).toBe(a)
    const m = subNode('m', 'ai_tool')
    expect(withAiPorts(m, sub('ai_tool'))).toBe(m)
    const plain = mainNode('p', { subInputs: [], subOutput: '' })
    expect(withAiPorts(plain, { fields: [] })).toBe(plain)
  })
})

describe('kind-checked connections', () => {
  const a = agent()
  const model = subNode('model', 'ai_languageModel', 'OpenAI Chat')
  const model2 = subNode('model2', 'ai_languageModel')
  const tool = subNode('tool', 'ai_tool', 'Calculator')

  it('connects a sub-node only to an input of its own kind', () => {
    expect(checkSubConnection(model, a, 'ai_languageModel', []).ok).toBe(true)
    const wrong = checkSubConnection(tool, a, 'ai_languageModel', [])
    expect(wrong).toMatchObject({ ok: false, reason: 'kind' })
    expect(wrong.message).toMatch(/Chat Model/)
    expect(checkSubConnection(tool, a, 'ai_outputParser', [])).toMatchObject({ ok: false, reason: 'kind' })
    expect(checkSubConnection(a, a, 'ai_tool', [])).toMatchObject({ ok: false, reason: 'self' })
  })

  it('enforces max, and leaves an input without max unlimited', () => {
    const edges = [subEdge('model', 'agent', 'ai_languageModel')]
    const over = checkSubConnection(model2, a, 'ai_languageModel', edges)
    expect(over).toMatchObject({ ok: false, reason: 'max' })
    expect(over.message).toMatch(/at most 1 Chat Model/)
    expect(freeSubInput(a, 'ai_languageModel', edges)).toBeNull()

    const tools = ['t1', 't2', 't3', 't4'].map(id => subEdge(id, 'agent', 'ai_tool'))
    expect(checkSubConnection(tool, a, 'ai_tool', tools).ok).toBe(true)
    expect(freeSubInput(a, 'ai_tool', tools)?.handle).toBe('ai_tool')
    expect(attachedCount(tools, 'agent', 'ai_tool')).toBe(4)
  })

  it('refuses the same connection twice', () => {
    const edges = [subEdge('tool', 'agent', 'ai_tool')]
    expect(checkSubConnection(tool, a, 'ai_tool', edges)).toMatchObject({ ok: false, reason: 'duplicate' })
  })

  it('stores the kind as both handles', () => {
    expect(subEdge('tool', 'agent', 'ai_tool')).toMatchObject({ source: 'tool', sourcePortId: 'ai_tool', target: 'agent', targetPortId: 'ai_tool' })
    expect(isAiEdge(subEdge('tool', 'agent', 'ai_tool'))).toBe(true)
    expect(isAiEdge({ sourcePortId: 'main', targetPortId: 'in' })).toBe(false)
    expect(isAiEdge({ sourcePortId: undefined })).toBe(false)
  })
})

describe('missingRequired', () => {
  it('names each required sub-input with nothing connected', () => {
    const a = agent()
    expect(missingRequired(a, [])).toEqual(['Chat Model'])
    expect(missingRequired(a, [subEdge('m', 'agent', 'ai_languageModel')])).toEqual([])
    // A connection of another kind does not satisfy it.
    expect(missingRequired(a, [subEdge('t', 'agent', 'ai_tool')])).toEqual(['Chat Model'])
    expect(missingRequired(mainNode('x'), [])).toEqual([])
  })
})

describe('cycle check and loop detection ignore AI edges', () => {
  it('a sub-node connection never makes a cycle', () => {
    const nodes = [mainNode('a'), agent('b')]
    // a → b on the main flow, plus b feeding a as an AI sub-node (would close a loop if counted).
    const edges = [
      { source: 'a', sourcePortId: 'main', target: 'b', targetPortId: 'in' },
      subEdge('b', 'a', 'ai_tool'),
    ]
    expect(hasForbiddenCycle(nodes, edges)).toBe(false)
    // The same pair of edges on the main flow is a cycle.
    expect(hasForbiddenCycle(nodes, [edges[0], { source: 'b', sourcePortId: 'main', target: 'a', targetPortId: 'in' }])).toBe(true)
  })

  it('an AI edge into a loop node is not a loop back-edge', () => {
    const nodes = [{ id: 'L', subtype: 'core.split_in_batches', outputs: [{ id: 'loop' }, { id: 'done' }] }, { id: 'body', outputs: [{ id: 'main' }] }]
    const edges = [
      { source: 'L', sourcePortId: 'loop', target: 'body' },
      subEdge('body', 'L', 'ai_tool'),
    ]
    expect(loopBackEdges(nodes, edges).size).toBe(0)
  })
})

describe('autoLayout', () => {
  const opts = { nodeW: 180, nodeH: 80, gapX: 80, gapY: 60 }

  it('lays a plain workflow out as before: columns by depth, rows within', () => {
    const nodes = [mainNode('t'), mainNode('a'), mainNode('b'), mainNode('c')]
    const edges = [
      { source: 't', target: 'a' }, { source: 't', target: 'b' }, { source: 'a', target: 'c' },
    ]
    expect(autoLayout(nodes, edges, opts)).toEqual({
      t: { x: 60, y: 60 },
      a: { x: 320, y: 60 }, b: { x: 320, y: 200 },
      c: { x: 580, y: 60 },
    })
  })

  it('puts sub-nodes in a row under their root, in sub-input order, out of the main columns', () => {
    const t = mainNode('t')
    const a = agent('agent')
    const tool1 = subNode('tool1', 'ai_tool'), tool2 = subNode('tool2', 'ai_tool')
    const mem = subNode('mem', 'ai_memory'), model = subNode('model', 'ai_languageModel')
    const out = mainNode('out')
    const nodes = [t, tool1, a, tool2, mem, model, out]
    const edges = [
      { source: 't', target: 'agent' }, { source: 'agent', target: 'out' },
      subEdge('tool1', 'agent', 'ai_tool'), subEdge('tool2', 'agent', 'ai_tool'),
      subEdge('mem', 'agent', 'ai_memory'), subEdge('model', 'agent', 'ai_languageModel'),
    ]
    const pos = autoLayout(nodes, edges, { ...opts, boxH: () => 80 })
    expect(pos.t).toEqual({ x: 60, y: 60 })
    expect(pos.agent).toEqual({ x: 320, y: 60 })
    const rowY = 60 + 80 + 60
    expect(['model', 'mem', 'tool1', 'tool2'].map(id => pos[id])).toEqual([
      { x: 320, y: rowY }, { x: 580, y: rowY }, { x: 840, y: rowY }, { x: 1100, y: rowY },
    ])
    // The next column starts past the agent's sub-node row.
    expect(pos.out.x).toBeGreaterThan(1100 + 180)
    expect(pos.out.y).toBe(60)
  })

  it('stacks a column below a root\'s sub-node rows, and nests sub-nodes of sub-nodes', () => {
    const a = agent('agent')
    const other = mainNode('other')
    const vs = withAiPorts(mainNode('vs'), { sub_output: 'ai_tool', sub_inputs: [{ handle: 'ai_embedding', label: 'Embedding', max: 1 }] })
    const emb = subNode('emb', 'ai_embedding')
    const nodes = [a, other, vs, emb]
    const edges = [subEdge('vs', 'agent', 'ai_tool'), subEdge('emb', 'vs', 'ai_embedding')]
    const pos = autoLayout(nodes, edges, { ...opts, boxH: () => 80 })
    expect(pos.vs).toEqual({ x: 60, y: 200 })
    expect(pos.emb).toEqual({ x: 60, y: 340 })
    expect(pos.other.y).toBeGreaterThanOrEqual(340 + 80)
  })

  it('keeps a sub-node connected to two roots under the first', () => {
    const a1 = agent('a1'), a2 = agent('a2'), m = subNode('m', 'ai_languageModel')
    const kids = subChildren([a1, a2, m], [subEdge('m', 'a1', 'ai_languageModel'), subEdge('m', 'a2', 'ai_languageModel')])
    expect(kids.get('a1')).toEqual(['m'])
    expect(kids.get('a2')).toEqual([])
  })
})

describe('aiEdgePath', () => {
  it('leaves and enters vertically', () => {
    expect(aiEdgePath(100, 300, 150, 200)).toBe('M100,300 C100,250 150,250 150,200')
  })
})

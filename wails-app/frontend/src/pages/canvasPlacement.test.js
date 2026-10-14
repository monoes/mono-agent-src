import { describe, it, expect } from 'vitest'
import { fitCamera, placeAfter, autoConnect, panToShow, placeBelow, autoConnectSub, DEFAULT_CAMERA } from './canvasPlacement.js'

const nodeW = 220
const nodeH = () => 100
const opts = { nodeW, nodeH }
const node = (id, x, y, extra = {}) => ({ id, x, y, inputs: [{ id: 'in' }], outputs: [{ id: 'out' }, { id: 'error' }], ...extra })

describe('fitCamera', () => {
  it('keeps the default camera for an empty canvas or a view not laid out', () => {
    expect(fitCamera([], { width: 1000, height: 600 }, opts)).toEqual(DEFAULT_CAMERA)
    expect(fitCamera([node('a', 0, 0)], { width: 0, height: 0 }, opts)).toEqual(DEFAULT_CAMERA)
    expect(fitCamera([node('a', 0, 0)], undefined, opts)).toEqual(DEFAULT_CAMERA)
  })

  it('never zooms in past 100%, and puts a small workflow at the padding', () => {
    const cam = fitCamera([node('a', 300, 200)], { width: 1200, height: 800 }, opts)
    expect(cam).toEqual({ x: 60 - 300, y: 60 - 200, zoom: 1 })
  })

  it('zooms out so a wide workflow fits, every node inside the view', () => {
    const nodes = [0, 1, 2, 3, 4, 5].map(i => node(`n${i}`, i * 300, 100))
    const view = { width: 1000, height: 600 }
    const cam = fitCamera(nodes, view, opts)
    expect(cam.zoom).toBeLessThan(1)
    for (const n of nodes) {
      const left = cam.x + n.x * cam.zoom
      const right = cam.x + (n.x + nodeW) * cam.zoom
      expect(left).toBeGreaterThanOrEqual(0)
      expect(right).toBeLessThanOrEqual(view.width)
    }
  })

  it('stops at the minimum zoom', () => {
    const nodes = [node('a', 0, 0), node('b', 100000, 0)]
    expect(fitCamera(nodes, { width: 1000, height: 600 }, opts).zoom).toBe(0.25)
  })
})

describe('placeAfter', () => {
  it('puts the new node to the right of the anchor with a gap', () => {
    const a = node('a', 100, 50)
    expect(placeAfter(a, [a], node('b', 0, 0), opts)).toEqual({ x: 100 + nodeW + 80, y: 50 })
  })

  it('moves down past nodes already in that spot', () => {
    const a = node('a', 100, 50)
    const taken = node('t', 100 + nodeW + 80, 50)
    const pos = placeAfter(a, [a, taken], node('b', 0, 0), opts)
    expect(pos.x).toBe(100 + nodeW + 80)
    expect(pos.y).toBe(50 + 100 + 40)
  })
})

describe('autoConnect', () => {
  it('wires the anchor\'s main output into the new node\'s input', () => {
    expect(autoConnect(node('a', 0, 0), node('b', 0, 0), [])).toEqual({
      source: 'a', sourcePortIdx: 0, sourcePortId: 'out', target: 'b', targetPortIdx: 0, targetPortId: 'in',
    })
  })

  it('leaves an already connected main output alone', () => {
    expect(autoConnect(node('a', 0, 0), node('b', 0, 0), [{ source: 'a', sourcePortIdx: 0, target: 'c' }])).toBeNull()
    // Another output being connected does not count.
    expect(autoConnect(node('a', 0, 0), node('b', 0, 0), [{ source: 'a', sourcePortIdx: 1, target: 'c' }])).not.toBeNull()
  })

  it('does nothing without an output to start from or an input to end at', () => {
    expect(autoConnect(node('a', 0, 0, { outputs: [] }), node('b', 0, 0), [])).toBeNull()
    expect(autoConnect(node('a', 0, 0), node('t', 0, 0, { inputs: [] }), [])).toBeNull()
    expect(autoConnect(null, node('b', 0, 0), [])).toBeNull()
  })
})

describe('panToShow', () => {
  const view = { width: 1000, height: 600 }
  it('leaves the camera alone when the box is already in view', () => {
    const cam = { x: 0, y: 0, zoom: 1 }
    expect(panToShow(cam, { x: 100, y: 100, w: 220, h: 100 }, view)).toBe(cam)
  })
  it('pans just enough to bring a box past the right edge into view', () => {
    const cam = panToShow({ x: 0, y: 0, zoom: 0.5 }, { x: 2000, y: 100, w: 220, h: 100 }, view)
    expect(cam.zoom).toBe(0.5)
    expect(cam.x + (2000 + 220) * 0.5).toBe(1000 - 40)
    expect(cam.y).toBe(0)
  })
  it('pans down and up too', () => {
    expect(panToShow({ x: 0, y: 0, zoom: 1 }, { x: 100, y: 700, w: 220, h: 100 }, view).y).toBe(600 - 40 - 800)
    expect(panToShow({ x: 0, y: 0, zoom: 1 }, { x: 100, y: -200, w: 220, h: 100 }, view).y).toBe(240)
  })
})

describe('AI sub-node click placement', () => {
  const agent = node('agent', 100, 100, { subInputs: [
    { handle: 'ai_languageModel', label: 'Chat Model', max: 1, required: true },
    { handle: 'ai_tool', label: 'Tool', max: 0 },
  ] })
  const sub = (id, kind, x = 0, y = 0) => ({ id, x, y, inputs: [], outputs: [], subOutput: kind })

  it('connects a new sub-node to the selected root\'s matching free input', () => {
    expect(autoConnectSub(agent, sub('m', 'ai_languageModel'), [])).toMatchObject({
      source: 'm', sourcePortId: 'ai_languageModel', target: 'agent', targetPortId: 'ai_languageModel',
    })
    // Full (max 1), no such input, or not a sub-node: nothing.
    const full = [{ source: 'm0', sourcePortId: 'ai_languageModel', target: 'agent', targetPortId: 'ai_languageModel' }]
    expect(autoConnectSub(agent, sub('m', 'ai_languageModel'), full)).toBeNull()
    expect(autoConnectSub(agent, sub('e', 'ai_embedding'), [])).toBeNull()
    expect(autoConnectSub(agent, node('n', 0, 0), [])).toBeNull()
  })

  it('places it under the root, right of the sub-nodes already there', () => {
    expect(placeBelow(agent, [agent], [], sub('t1', 'ai_tool'), opts)).toEqual({ x: 100, y: 100 + 100 + 60 })
    const t1 = sub('t1', 'ai_tool', 100, 260)
    const edges = [{ source: 't1', sourcePortId: 'ai_tool', target: 'agent', targetPortId: 'ai_tool' }]
    expect(placeBelow(agent, [agent, t1], edges, sub('t2', 'ai_tool'), opts)).toEqual({ x: 100 + nodeW + 40, y: 260 })
  })

  it('moves right past a node in the way', () => {
    const other = node('other', 120, 270)
    const p = placeBelow(agent, [agent, other], [], sub('t1', 'ai_tool'), opts)
    expect(p).toEqual({ x: 120 + nodeW + 40, y: 260 })
  })
})

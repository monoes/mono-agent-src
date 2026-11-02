import { describe, expect, it } from 'vitest'
import { MAX_TITLE_RUNES, hasSessionTree, sessionTree, titleProblem, visibleRows } from './taskSessionModel.js'

const s = (id, parent) => ({ id, title: `S${id}`, ...(parent ? { parent_session_id: parent } : {}) })
const shape = (nodes) => nodes.map(n => [n.session.id, n.depth, shape(n.children)])

describe('sessionTree', () => {
  it('is empty for no sessions', () => {
    expect(sessionTree([])).toEqual([])
    expect(sessionTree(undefined)).toEqual([])
  })
  it('keeps a flat list as roots in order', () => {
    expect(shape(sessionTree([s(3), s(1), s(2)]))).toEqual([[3, 1, []], [1, 1, []], [2, 1, []]])
    expect(hasSessionTree([s(1), s(2)])).toBe(false)
  })
  it('nests children under their parent with depths', () => {
    const t = sessionTree([s(1), s(2, 1), s(3, 2), s(4, 1)])
    expect(shape(t)).toEqual([[1, 1, [[2, 2, [[3, 3, []]]], [4, 2, []]]]])
    expect(hasSessionTree([s(1), s(2, 1)])).toBe(true)
  })
  it('makes a session with a missing parent a root', () => {
    expect(shape(sessionTree([s(5, 99), s(6, 5)]))).toEqual([[5, 1, [[6, 2, []]]]])
  })
  it('breaks a ring and a self-parent so every session shows once', () => {
    expect(shape(sessionTree([s(1, 2), s(2, 1)]))).toEqual([[1, 1, [[2, 2, []]]]])
    expect(shape(sessionTree([s(1, 1)]))).toEqual([[1, 1, []]])
    const t = sessionTree([s(1, 3), s(2, 1), s(3, 2), s(4, 3)])
    expect(shape(t)).toEqual([[1, 1, [[2, 2, [[3, 3, [[4, 4, []]]]]]]]])
  })
})

describe('visibleRows', () => {
  it('skips the children of collapsed sessions', () => {
    const t = sessionTree([s(1), s(2, 1), s(3, 2)])
    expect(visibleRows(t, new Set()).map(r => r.session.id)).toEqual([1, 2, 3])
    const rows = visibleRows(t, new Set([2]))
    expect(rows.map(r => [r.session.id, r.hasChildren, r.expanded, r.parentId])).toEqual([[1, true, true, null], [2, true, false, 1]])
    expect(visibleRows(t, new Set([1])).map(r => r.session.id)).toEqual([1])
  })
})

describe('titleProblem', () => {
  it('refuses empty and too long, counting characters', () => {
    expect(titleProblem('  ')).toBe('empty')
    expect(titleProblem('ok')).toBe('')
    expect(titleProblem('é'.repeat(MAX_TITLE_RUNES))).toBe('')
    expect(titleProblem('é'.repeat(MAX_TITLE_RUNES + 1))).toBe('tooLong')
  })
})

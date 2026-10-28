import { describe, it, expect } from 'vitest'
import {
  STATUS, emptySummary, applyChatEvent, summaryFromTurns, totalCost, costEstimated, layoutBubbles, moveBubble,
  loadState, saveState, STORAGE_KEY, shouldCollapseOnBackdrop, monogram, newDraftKey, orgBubble, isOrgBubble,
  profileFolderName,
} from './coderBubbles.js'

const ev = (type, turnId, payload = {}, at = '2026-09-29T10:00:00Z') => ({ type, turnId, payload, at, conversationId: 'c1' })

describe('applyChatEvent', () => {
  it('runs a turn from start to done and counts it unread while collapsed', () => {
    let s = applyChatEvent(emptySummary(), ev('turn.started', 't1'), false)
    expect(s.status).toBe(STATUS.working)
    expect(s.activeTurnId).toBe('t1')
    s = applyChatEvent(s, ev('usage.updated', 't1', { costUsd: 0.01 }), false)
    s = applyChatEvent(s, ev('usage.updated', 't1', { costUsd: 0.03 }), false)
    expect(totalCost(s)).toBeCloseTo(0.03) // a snapshot per turn, not summed
    s = applyChatEvent(s, ev('turn.finished', 't1', { status: 'completed' }), false)
    expect(s.status).toBe(STATUS.done)
    expect(s.unread).toBe(1)
    expect(s.activeTurnId).toBe('')
  })

  it('does not count unread while expanded, and maps failure and stop', () => {
    let s = applyChatEvent(emptySummary(), ev('turn.started', 't1'), true)
    s = applyChatEvent(s, ev('turn.finished', 't1', { status: 'failed' }), true)
    expect(s.status).toBe(STATUS.error)
    expect(s.unread).toBe(0)
    s = applyChatEvent(s, ev('turn.started', 't2'), true)
    s = applyChatEvent(s, ev('turn.finished', 't2', { status: 'cancelled' }), true)
    expect(s.status).toBe(STATUS.idle)
    s = applyChatEvent(s, ev('turn.started', 't3'), true)
    s = applyChatEvent(s, ev('turn.finished', 't3', { status: 'interrupted' }), true)
    expect(s.status).toBe(STATUS.error)
  })

  it('ignores a late finish for an older turn while a newer one runs', () => {
    let s = applyChatEvent(emptySummary(), ev('turn.started', 't2'), false)
    s = applyChatEvent(s, ev('turn.finished', 't1', { status: 'completed' }), false)
    expect(s.status).toBe(STATUS.working)
    expect(s.activeTurnId).toBe('t2')
  })

  it('sums costs across turns and ignores unrelated events', () => {
    let s = applyChatEvent(emptySummary(), ev('usage.updated', 't1', { costUsd: 0.02 }), false)
    s = applyChatEvent(s, ev('usage.updated', 't2', { costUsd: 0.05 }), false)
    expect(totalCost(s)).toBeCloseTo(0.07)
    expect(applyChatEvent(s, ev('assistant.delta', 't2'), false)).toBe(s)
    expect(applyChatEvent(s, ev('usage.updated', 't2', {}), false)).toBe(s)
  })
})

describe('summaryFromTurns', () => {
  it('re-attaches to a running turn and shows a failed last turn', () => {
    expect(summaryFromTurns([{ id: 't9', status: 'active', createdAt: 'x' }]).status).toBe(STATUS.working)
    expect(summaryFromTurns([{ id: 't9', status: 'active', ownedByThisInstance: false }]).status).toBe(STATUS.idle)
    expect(summaryFromTurns([{ id: 't9', status: 'failed' }]).status).toBe(STATUS.error)
    expect(summaryFromTurns([{ id: 't9', status: 'completed' }]).status).toBe(STATUS.idle)
    expect(summaryFromTurns([]).status).toBe(STATUS.idle)
  })
})

describe('layoutBubbles and moveBubble', () => {
  const b = n => Array.from({ length: n }, (_, i) => ({ key: `k${i}` }))
  it('keeps everything visible up to the max', () => {
    expect(layoutBubbles(b(6), '').overflow).toHaveLength(0)
  })
  it('overflows past the max and keeps the expanded bubble visible', () => {
    const list = b(9)
    const { visible, overflow } = layoutBubbles(list, '')
    expect(visible).toHaveLength(5)
    expect(overflow).toHaveLength(4)
    const withExp = layoutBubbles(list, 'k8')
    expect(withExp.visible.map(x => x.key)).toContain('k8')
    expect(withExp.visible).toHaveLength(5)
    expect(withExp.visible.length + withExp.overflow.length).toBe(9)
  })
  it('moves a bubble', () => {
    expect(moveBubble(b(3), 'k2', 'k0').map(x => x.key)).toEqual(['k2', 'k0', 'k1'])
    expect(moveBubble(b(3), 'nope', 'k0').map(x => x.key)).toEqual(['k0', 'k1', 'k2'])
  })
})

describe('persistence', () => {
  const memory = () => {
    const m = new Map()
    return { getItem: k => m.get(k) ?? null, setItem: (k, v) => m.set(k, v), m }
  }
  it('keeps real conversations and the dock side, drops drafts', () => {
    const s = memory()
    saveState({ side: 'left', bubbles: [{ key: 'draft-1', conversationId: '' }, { key: 'c1', conversationId: 'c1', cwd: '/w', model: 'opus', runtime: 'codex' }] }, s)
    expect(loadState(s)).toEqual({ side: 'left', bubbles: [{ key: 'c1', conversationId: 'c1', cwd: '/w', model: 'opus', runtime: 'codex', access: '', title: '', effort: '', orgMode: '', taskId: '', taskKind: '' }] })
  })
  it('survives throwing or corrupt storage', () => {
    const throwing = { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }
    expect(loadState(throwing)).toEqual({ bubbles: [], side: 'right' })
    expect(() => saveState({ bubbles: [], side: 'right' }, throwing)).not.toThrow()
    const s = memory()
    s.setItem(STORAGE_KEY, '{nope')
    expect(loadState(s)).toEqual({ bubbles: [], side: 'right' })
  })
})

describe('shouldCollapseOnBackdrop', () => {
  it.each([
    [{ pressStartedOnBackdrop: true, selectionText: '', modalOpen: false }, true],
    [{ pressStartedOnBackdrop: false, selectionText: '', modalOpen: false }, false], // drag that ended outside
    [{ pressStartedOnBackdrop: true, selectionText: 'copied text', modalOpen: false }, false],
    [{ pressStartedOnBackdrop: true, selectionText: '  ', modalOpen: false }, true],
    [{ pressStartedOnBackdrop: true, selectionText: '', modalOpen: true }, false],
  ])('%o → %s', (input, want) => {
    expect(shouldCollapseOnBackdrop(input)).toBe(want)
  })
})

describe('monogram and draft keys', () => {
  it('makes two-letter labels', () => {
    expect(monogram('20260927-brisk-otter')).toBe('BO')
    expect(monogram('mono-agent')).toBe('MA')
    expect(monogram('api')).toBe('AP')
    expect(monogram('')).toBe('·')
  })
  it('makes unique draft keys', () => {
    expect(newDraftKey()).not.toBe(newDraftKey())
  })
})

describe('profileFolderName (#477)', () => {
  const profiles = [
    { id: '0f3c9a1e-77b2-4c55-9d1a-6e2f4b8c1d20', name: 'Work', root_dir: '/home/u/.monoagent/profiles/0f3c9a1e-77b2-4c55-9d1a-6e2f4b8c1d20' },
    { id: 'p-home', name: 'Home', root_dir: '/home/u/notes/' },
  ]
  it("names a profile's own folder by the profile", () => {
    expect(profileFolderName('/home/u/.monoagent/profiles/0f3c9a1e-77b2-4c55-9d1a-6e2f4b8c1d20', profiles)).toBe('Work')
    expect(profileFolderName('/home/u/notes', profiles)).toBe('Home')
    expect(profileFolderName('/home/u/.monoagent/profiles/0f3c9a1e-77b2-4c55-9d1a-6e2f4b8c1d20/', profiles)).toBe('Work')
  })
  it('names nothing else, not even a folder inside a profile', () => {
    expect(profileFolderName('/home/u/notes/app', profiles)).toBe('')
    expect(profileFolderName('/home/u/monoagent-coder/brisk-otter', profiles)).toBe('')
    expect(profileFolderName('', profiles)).toBe('')
    expect(profileFolderName('/x', null)).toBe('')
    expect(profileFolderName('/x', [{ id: 'a', name: 'A' }])).toBe('')
  })
})

describe('nowDoing', () => {
  it('names the latest running call and its target', async () => {
    const { nowDoing } = await import('./coderBubbles.js')
    expect(nowDoing({ calls: {} })).toBe('')
    expect(nowDoing({ calls: { a: { name: 'Read', status: 'completed', arguments: { file_path: 'x' } } } })).toBe('')
    expect(nowDoing({ calls: { a: { name: 'Edit', status: 'started', arguments: { file_path: 'src/app.go' } } } })).toBe('Edit · src/app.go')
    expect(nowDoing({ calls: { a: { name: 'Bash', status: 'started', arguments: '{"command":"go   test ./..."}' } } })).toBe('Bash · go test ./...')
    expect(nowDoing({ calls: { a: { name: 'Think', status: 'started', arguments: null } } })).toBe('Think')
  })
})

describe('applyChatEvent: a dynamic-org agent asking the user (#228)', () => {
  it('pulses the bubble until that agent moves on', () => {
    let s = applyChatEvent(emptySummary(), ev('turn.started', 't1'), false)
    s = applyChatEvent(s, ev('agent.message', 't1', { agentId: 'w1', direction: 'result', text: 'x' }), false)
    expect(s.status).toBe(STATUS.working)
    s = applyChatEvent(s, ev('agent.message', 't1', { agentId: 'w1', direction: 'question', text: 'Which cache?' }), false)
    expect(s.status).toBe(STATUS.needs)
    s = applyChatEvent(s, ev('agent.status', 't1', { agentId: 'w2', to: 'done' }), false)
    expect(s.status).toBe(STATUS.needs)
    s = applyChatEvent(s, ev('agent.status', 't1', { agentId: 'w1', to: 'working' }), false)
    expect(s.status).toBe(STATUS.working)
  })

  it('ignores a question from another turn', () => {
    const s = applyChatEvent(applyChatEvent(emptySummary(), ev('turn.started', 't2'), false), ev('agent.message', 't1', { agentId: 'w1', direction: 'question' }), false)
    expect(s.status).toBe(STATUS.working)
  })
})

describe('applyChatEvent: worker usage (#257)', () => {
  it('adds a worker\'s cost to the lead\'s instead of replacing it', () => {
    let s = applyChatEvent(emptySummary(), ev('turn.started', 't1'), false)
    s = applyChatEvent(s, ev('usage.updated', 't1', { costUsd: 0.1 }), false)
    s = applyChatEvent(s, ev('usage.updated', 't1', { agentId: 'w1', costUsd: 0.02 }), false)
    s = applyChatEvent(s, ev('usage.updated', 't1', { agentId: 'w1', costUsd: 0.03 }), false)
    s = applyChatEvent(s, ev('usage.updated', 't1', { costUsd: 0.12 }), false)
    expect(totalCost(s)).toBeCloseTo(0.15)
    expect(costEstimated(s)).toBe(false)
  })

  it('marks the cost estimated while a worker\'s is estimated from its tokens (#230)', () => {
    let s = applyChatEvent(emptySummary(), ev('usage.updated', 't1', { costUsd: 0.1 }), false)
    expect(costEstimated(s)).toBe(false)
    s = applyChatEvent(s, ev('usage.updated', 't1', { agentId: 'w1', costUsd: 0.35, costEstimated: true }), false)
    expect(totalCost(s)).toBeCloseTo(0.45)
    expect(costEstimated(s)).toBe(true)
    // Its runtime reported a real cost after all: no longer estimated.
    s = applyChatEvent(s, ev('usage.updated', 't1', { agentId: 'w1', costUsd: 0.3 }), false)
    expect(costEstimated(s)).toBe(false)
    expect(costEstimated(emptySummary())).toBe(false)
  })
})

describe('org bubbles (#229)', () => {
  function memoryStorage() {
    const m = new Map()
    return { getItem: k => m.get(k) ?? null, setItem: (k, v) => m.set(k, String(v)) }
  }
  it('are kept across restarts next to coder chats, in order', () => {
    const store = memoryStorage()
    const coder = { key: 'c1', conversationId: 'c1', cwd: '/w/a', model: 'm', runtime: 'claude' }
    saveState({ bubbles: [coder, orgBubble('acme'), { key: 'draft-1', conversationId: '' }], side: 'left' }, store)
    const { bubbles, side } = loadState(store)
    expect(side).toBe('left')
    expect(bubbles.map(b => b.key)).toEqual(['c1', 'org:acme'])
    expect(isOrgBubble(bubbles[1])).toBe(true)
    expect(bubbles[1].orgName).toBe('acme')
  })
  it('drops a malformed org entry', () => {
    const store = memoryStorage()
    store.setItem(STORAGE_KEY, JSON.stringify({ bubbles: [{ kind: 'org' }, { kind: 'org', orgName: 7 }, { kind: 'org', orgName: 'ok' }] }))
    expect(loadState(store).bubbles.map(b => b.key)).toEqual(['org:ok'])
  })

  describe('chat bubbles with access and title', () => {
    const mem = () => { const m = {}; return { getItem: k => m[k] ?? null, setItem: (k, v) => { m[k] = v }, raw: m } }

    it('round-trips access, title, effort, org mode and the task kind', () => {
      const s = mem()
      saveState({ side: 'right', bubbles: [{ key: 'c1', conversationId: 'c1', cwd: '', model: 'm', runtime: 'claude', access: 'scoped', title: 'Fix the bug', effort: 'high', orgMode: 'dynamic', taskId: 't1', taskKind: 'application-apply' }] }, s)
      expect(loadState(s).bubbles[0]).toMatchObject({ access: 'scoped', title: 'Fix the bug', effort: 'high', orgMode: 'dynamic', taskId: 't1', taskKind: 'application-apply' })
    })

    it('reads a v1 entry written before levels as an unknown level, keeping its old fields', () => {
      const s = mem()
      s.setItem(STORAGE_KEY, JSON.stringify({ side: 'right', bubbles: [{ conversationId: 'old', cwd: '/w', model: 'opus', runtime: 'claude' }] }))
      expect(loadState(s).bubbles[0]).toMatchObject({ key: 'old', conversationId: 'old', cwd: '/w', model: 'opus', runtime: 'claude', access: '' })
    })

    it('does not trust a stored level it does not know', () => {
      const s = mem()
      s.setItem(STORAGE_KEY, JSON.stringify({ bubbles: [{ conversationId: 'x', access: 'root' }] }))
      expect(loadState(s).bubbles[0].access).toBe('')
    })

    it('keeps the four original fields on every saved bubble', () => {
      const s = mem()
      saveState({ side: 'right', bubbles: [{ key: 'c1', conversationId: 'c1', cwd: '/w', model: 'm', runtime: 'r', access: 'coding' }] }, s)
      expect(JSON.parse(s.raw[STORAGE_KEY]).bubbles[0]).toMatchObject({ conversationId: 'c1', cwd: '/w', model: 'm', runtime: 'r' })
    })
  })
})

describe('bubble storage per profile', () => {
  it('keeps each profile its own list', () => {
    const mem = {}
    const storage = { getItem: k => mem[k] ?? null, setItem: (k, v) => { mem[k] = v } }
    saveState({ bubbles: [{ conversationId: 'c1' }], side: 'right' }, storage, 'a')
    expect(loadState(storage, 'b').bubbles).toEqual([])
    expect(loadState(storage, 'a').bubbles.map(b => b.key)).toEqual(['c1'])
  })
})

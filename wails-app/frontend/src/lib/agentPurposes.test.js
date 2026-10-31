import { describe, it, expect } from 'vitest'
import contract from '../components/settings/__fixtures__/agentPurposes.contract.json'
import {
  parsePurposes, rosterStatus, groupRoster, draftFrom, isDirty, setArgs, budgetError, effortOptions, signature,
} from './agentPurposes.js'

describe('parsePurposes (the agent purposes list contract)', () => {
  const doc = parsePurposes(contract)
  it('reads the 18 purposes and the roster', () => {
    expect(doc.purposes.map(p => p.id)).toEqual([
      'coding', 'debugging', 'research', 'deep-research', 'planning', 'strategy', 'review', 'testing', 'security-audit',
      'writing', 'data', 'automation', 'browsing', 'design', 'triage', 'judging', 'orchestrating', 'translation',
    ])
    expect(doc.roster).toHaveLength(7)
    const review = doc.purposes.find(p => p.id === 'review')
    expect(review).toMatchObject({ rule: 'order', effort: 'high', differentFromAuthor: true, fallback: 'none', budgetSharePct: 15, isDefault: false })
    expect(review.pairs.map(p => `${p.runtime}:${p.model}:${p.status}`)).toEqual(['codex:gpt-5:ok', 'claude:opus:ok'])
    expect(doc.purposes.find(p => p.id === 'writing')).toMatchObject({ pairs: [], isDefault: true, rule: 'any', fallback: 'any' })
  })
  it('refuses a reply that is not the contract', () => {
    expect(() => parsePurposes({})).toThrow()
    expect(() => parsePurposes(null)).toThrow()
    expect(() => parsePurposes({ purposes: 'x' })).toThrow()
  })
  it('treats an unknown status or rule as the safe value', () => {
    const d = parsePurposes({ purposes: [{ id: 'x', rule: 'weird', fallback: '?', pairs: [{ runtime: 'a', model: 'b', status: 'ready' }] }], roster: [] })
    expect(d.purposes[0]).toMatchObject({ rule: 'any', fallback: 'any' })
    expect(d.purposes[0].pairs[0].status).toBe('unvalidated')
  })
})

describe('roster helpers', () => {
  const { roster } = parsePurposes(contract)
  it('takes a pair status from the roster, and a missing pair is unvalidated', () => {
    expect(rosterStatus(roster, { runtime: 'claude', model: 'haiku' })).toBe('stale')
    expect(rosterStatus(roster, { runtime: 'claude', model: 'gone' })).toBe('unvalidated')
  })
  it('groups by system keeping order', () => {
    expect(groupRoster(roster).map(g => `${g.runtime}:${g.models.length}`)).toEqual(['claude:3', 'codex:2', 'ollama:1', 'opencode:1'])
  })
})

describe('drafts', () => {
  const { purposes } = parsePurposes(contract)
  const review = purposes.find(p => p.id === 'review')
  it('is clean until changed', () => {
    const d = draftFrom(review)
    expect(isDirty(d, review)).toBe(false)
    expect(isDirty({ ...d, rule: 'any' }, review)).toBe(true)
    expect(isDirty({ ...d, pairs: d.pairs.slice().reverse() }, review)).toBe(true)
    expect(signature(d)).toBe(signature(draftFrom(review)))
  })
  it('builds the save arguments, a model id with colons intact', () => {
    const d = { ...draftFrom(review), pairs: [{ runtime: 'ollama', model: 'llama3:8b' }], budgetShare: ' 12.5 ' }
    expect(setArgs('review', d)).toEqual(['review', ['ollama:llama3:8b'], 'order', 'high', true, 'none', 12.5])
    expect(setArgs('writing', draftFrom(purposes.find(p => p.id === 'writing')))).toEqual(['writing', [], 'any', '', false, 'any', 0])
  })
  it('checks the budget share', () => {
    expect(budgetError('')).toBe('')
    expect(budgetError('20')).toBe('')
    expect(budgetError('100.0')).toBe('')
    expect(budgetError('101')).toBe('range')
    expect(budgetError('1e2')).toBe('number')
    expect(budgetError('abc')).toBe('number')
    expect(budgetError('-1')).toBe('number')
  })
  it('keeps an unknown saved effort selectable', () => {
    expect(effortOptions('turbo')).toContain('turbo')
    expect(effortOptions('high')).not.toContain('turbo')
  })
})

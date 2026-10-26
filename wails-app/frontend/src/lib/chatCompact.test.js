import { describe, it, expect } from 'vitest'
import { COMPACT_PROMPT, compactSummary, compactSeed } from './chatCompact.js'

const text = t => ({ kind: 'text', text: t })

describe('compactSummary', () => {
  it('joins the text parts of a completed turn and trims', () => {
    const turn = { terminal: { status: 'completed' }, parts: [text(' Goal: ship. '), { kind: 'tool', name: 'x' }, text('Open: tests ')] }
    expect(compactSummary(turn)).toBe('Goal: ship. \nOpen: tests')
  })
  it('is empty for a failed or cancelled turn', () => {
    expect(compactSummary({ terminal: { status: 'failed' }, parts: [text('partial')] })).toBe('')
    expect(compactSummary({ terminal: { status: 'cancelled' }, parts: [text('partial')] })).toBe('')
  })
  it('is empty when the turn is missing, unfinished or said nothing', () => {
    expect(compactSummary(null)).toBe('')
    expect(compactSummary({ terminal: null, parts: [text('x')] })).toBe('')
    expect(compactSummary({ terminal: { status: 'completed' } })).toBe('')
    expect(compactSummary({ terminal: { status: 'completed' }, parts: [text('  ')] })).toBe('')
  })
})

describe('compact prompts', () => {
  it('asks for the summary only, and the seed carries it over', () => {
    expect(COMPACT_PROMPT).toMatch(/summary only/i)
    expect(compactSeed('S1')).toContain('S1')
  })
})

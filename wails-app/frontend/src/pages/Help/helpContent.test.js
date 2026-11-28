import { describe, it, expect } from 'vitest'
import { TOPICS, PIT_STAGES, EASTER_EGGS, PAGE_IDS, HELP_KEYS } from './helpContent.js'

const REQUIRED = ['firstSteps', 'profiles', 'connections', 'workflows', 'tasks', 'hil', 'cards', 'orgs', 'coderModes', 'extension', 'vault', 'apiGateway', 'images', 'troubleshooting', 'updates']

describe('helpContent', () => {
  it('covers every required topic with unique ids', () => {
    const ids = TOPICS.map(t => t.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const id of REQUIRED) expect(ids).toContain(id)
  })
  it('every topic has title, blurb, emoji, quip and at least one action', () => {
    for (const t of TOPICS) {
      for (const f of ['title', 'blurb', 'emoji', 'quip']) expect(t[f], `${t.id}.${f}`).toBeTruthy()
      expect(t.steps.some(s => s.action), t.id).toBe(true)
    }
  })
  it('no step text is empty and every action is well formed', () => {
    for (const t of TOPICS) for (const s of t.steps) {
      expect(s.text.trim().length).toBeGreaterThan(0)
      if (!s.action) continue
      if (s.action.kind === 'goto') {
        expect(PAGE_IDS, `${t.id}: ${s.action.target}`).toContain(s.action.target)
      } else {
        expect(s.action.kind).toBe('coder')
        expect(s.action.prompt.length).toBeGreaterThan(60)
      }
    }
  })
  it('goto params use only keys the pages accept', () => {
    const allowed = { dashboard: ['showGetStarted'], settings: ['section'], connections: ['tab', 'automationId'], noderunner: ['openTemplates', 'workflowId', 'executionId'], orgs: ['tab', 'org', 'run'] }
    const sections = ['health', 'jev', 'version']
    for (const t of TOPICS) for (const s of t.steps) {
      const a = s.action
      if (a?.kind !== 'goto' || !a.params) continue
      for (const k of Object.keys(a.params)) expect(allowed[a.target] || [], `${t.id}.${k}`).toContain(k)
      if (a.target === 'settings') expect(sections).toContain(a.params.section)
    }
  })
  it('danger levels are 1-5 with a quip', () => {
    for (const t of TOPICS) if (t.danger) {
      expect(t.danger.level).toBeGreaterThanOrEqual(1)
      expect(t.danger.level).toBeLessThanOrEqual(5)
      expect(t.danger.quip).toBeTruthy()
    }
  })
  it('pit escalates in order and ends at full access', () => {
    const levels = PIT_STAGES.map(s => s.level)
    expect(levels).toEqual([...levels].sort((a, b) => a - b))
    for (const s of PIT_STAGES) for (const f of ['permission', 'label', 'consequence', 'mascotMood']) expect(s[f]).toBeTruthy()
    expect(PIT_STAGES.at(-1).consequence).toMatch(/any command/)
  })
  it('has at least 12 unique easter eggs', () => {
    expect(EASTER_EGGS.length).toBeGreaterThanOrEqual(12)
    expect(new Set(EASTER_EGGS.map(e => e.trigger)).size).toBe(EASTER_EGGS.length)
    for (const e of EASTER_EGGS) expect(e.line).toBeTruthy()
  })
  it('exports a flat key map with no empty English', () => {
    const keys = Object.keys(HELP_KEYS)
    expect(keys.length).toBeGreaterThan(100)
    for (const k of keys) { expect(k).toMatch(/^help\./); expect(HELP_KEYS[k].trim()).not.toBe('') }
  })
})

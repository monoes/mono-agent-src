import { describe, expect, it } from 'vitest'
import { MISSION_TEMPLATES, insertTemplate, looksLikeMission, templateText } from './missionTemplates.js'

describe('mission templates', () => {
  it('has the five kinds, each with goal, constraints, acceptance and non-goals', () => {
    expect(MISSION_TEMPLATES.map(t => t.id)).toEqual(['feature', 'bugfix', 'refactor', 'research', 'migration'])
    for (const t of MISSION_TEMPLATES) for (const w of ['Goal:', 'Constraints:', 'Acceptance:', 'Non-goals:']) expect(t.text).toContain(w)
  })
  it('fills an empty composer, and goes after typed text', () => {
    expect(insertTemplate('', 'bugfix')).toBe(templateText('bugfix'))
    expect(insertTemplate('  fix login ', 'bugfix')).toBe(`fix login\n\n${templateText('bugfix')}`)
    expect(insertTemplate('x', 'nope')).toBe('x')
  })
  it('guesses a mission-like first message', () => {
    expect(looksLikeMission('fix typo')).toBe(false)
    expect(looksLikeMission('')).toBe(false)
    expect(looksLikeMission('please migrate the whole billing module from the old ORM to the new query builder and keep tests green')).toBe(true)
    expect(looksLikeMission(Array(45).fill('word').join(' '))).toBe(true)
  })
})

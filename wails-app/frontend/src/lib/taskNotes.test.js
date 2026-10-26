import { describe, it, expect } from 'vitest'
import { splitNotes, joinNotes, metaFacts } from './taskNotes.js'

const LINE = '[mission-meta] {"files":["a.go","b.go"],"size":"m","risk":"low"}'

describe('splitNotes', () => {
  it('splits the closing meta line from the text', () => {
    const r = splitNotes(`Do the thing.\n\n${LINE}`)
    expect(r.text).toBe('Do the thing.')
    expect(r.meta).toEqual({ files: ['a.go', 'b.go'], size: 'm', risk: 'low' })
    expect(r.line).toBe(LINE)
  })
  it('reads a trailing newline after the line like the CLI does', () => {
    expect(splitNotes(`x\n${LINE}\n`).text).toBe('x')
  })
  it('leaves notes without a meta line alone', () => {
    expect(splitNotes('plain\nnotes')).toEqual({ text: 'plain\nnotes', meta: null, line: '' })
    expect(splitNotes(null).text).toBe('')
  })
  it('only the LAST line counts', () => {
    const r = splitNotes(`${LINE}\nmore text`)
    expect(r.meta).toBeNull()
    expect(r.text).toBe(`${LINE}\nmore text`)
  })
  it('drops an empty object from the display but keeps the line', () => {
    const r = splitNotes('Epic text\n\n[mission-meta] {}')
    expect(r).toEqual({ text: 'Epic text', meta: null, line: '[mission-meta] {}' })
  })
  it('shows malformed JSON as plain text', () => {
    for (const bad of ['[mission-meta] {"files":', '[mission-meta] [1]', '[mission-meta] 7', '[mission-meta] null']) {
      const n = `text\n\n${bad}`
      expect(splitNotes(n)).toEqual({ text: n, meta: null, line: '' })
    }
  })
  it('takes a notes body that is only the line', () => {
    expect(splitNotes(LINE).text).toBe('')
  })
})

describe('joinNotes', () => {
  it('round-trips: unchanged text gives the notes back', () => {
    const notes = `Do the thing.\n\n${LINE}`
    const { text, line } = splitNotes(notes)
    expect(joinNotes(text, line)).toBe(notes)
  })
  it('re-appends the line once, unchanged, after an edit', () => {
    const { line } = splitNotes(`old\n\n${LINE}`)
    const saved = joinNotes('new text\n', line)
    expect(saved).toBe(`new text\n\n${LINE}`)
    const again = splitNotes(saved)
    expect(joinNotes(again.text, again.line)).toBe(saved)
    expect(saved.split('[mission-meta]').length).toBe(2)
  })
  it('keeps the line when the text is emptied', () => {
    expect(joinNotes('', LINE)).toBe(LINE)
  })
  it('adds nothing when there was no line', () => {
    expect(joinNotes('abc', '')).toBe('abc')
  })
})

describe('metaFacts', () => {
  it('reads files, size and risk, and ignores other shapes', () => {
    expect(metaFacts({ files: ['a', '', 3], size: 's', risk: {} })).toEqual({ files: ['a'], size: 's', risk: '' })
    expect(metaFacts(null)).toEqual({ files: [], size: '', risk: '' })
  })
})

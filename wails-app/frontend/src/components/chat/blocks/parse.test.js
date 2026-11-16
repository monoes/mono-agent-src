import { describe, it, expect } from 'vitest'
import { parseBlock, infoFromClassName } from './parse.js'
import { LIM, Bad, str, link, depthOk } from './schema.js'

const reg = { demo: (j) => ({ t: str(j.t) }) }

describe('infoFromClassName', () => {
  it('reads the language from react-markdown classes', () => {
    expect(infoFromClassName('language-choices')).toBe('choices')
    expect(infoFromClassName('foo language-chart')).toBe('chart')
    expect(infoFromClassName(undefined)).toBe('')
    expect(infoFromClassName('hljs')).toBe('')
  })
})

describe('parseBlock', () => {
  it('ignores unknown types, including prototype names', () => {
    expect(parseBlock('js', '{}', reg).status).toBe('none')
    expect(parseBlock('constructor', '{}', reg).status).toBe('none')
    expect(parseBlock('__proto__', '{}', reg).status).toBe('none')
  })
  it('accepts a valid body', () => {
    expect(parseBlock('demo', '{"t":"hi"}\n', reg)).toEqual({ status: 'ok', type: 'demo', data: { t: 'hi' } })
  })
  it('marks unparsable JSON incomplete (streaming)', () => {
    expect(parseBlock('demo', '{"t":"h', reg).status).toBe('incomplete')
  })
  it('rejects schema failures, oversize and too deep bodies', () => {
    expect(parseBlock('demo', '{"t":5}', reg).status).toBe('invalid')
    expect(parseBlock('demo', '{"t":"' + 'x'.repeat(LIM.body) + '"}', reg).status).toBe('invalid')
    const deep = '{"t":"a","d":' + '['.repeat(20) + ']'.repeat(20) + '}'
    expect(parseBlock('demo', deep, reg).status).toBe('invalid')
  })
  it('lets non-validation errors surface', () => {
    expect(() => parseBlock('demo', '{}', { demo: () => { throw new TypeError('x') } })).toThrow(TypeError)
  })
})

describe('schema primitives', () => {
  it('strips control and bidi characters and enforces length', () => {
    expect(str('a‮b\nc')).toBe('a b c')
    expect(() => str('x'.repeat(201))).toThrow(Bad)
    expect(() => str('   ')).toThrow(Bad)
  })
  it('accepts only app links', () => {
    expect(link('monoagent://workflow/abc-1')).toBe('monoagent://workflow/abc-1')
    for (const v of ['https://x.com', 'javascript:alert(1)', 'monoagent://Workflow/x', 'monoagent://w/', 'monoagent://w/a b', 5]) expect(() => link(v)).toThrow(Bad)
  })
  it('measures depth', () => {
    expect(depthOk({ a: [{ b: 1 }] })).toBe(true)
    expect(depthOk([[[[[[[1]]]]]]])).toBe(false)
  })
})

// @vitest-environment jsdom
// The command an Approve releases must not read as something else: invisible
// and reordering characters show as escapes, text monomind hid is warned
// about, and a box that does not show it all says so.
import React from 'react'
import { describe, it, expect, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import '../i18n.js'
import en from '../locales/en.json'
import es from '../locales/es.json'
import { ApprovalCommand, ApprovalCall, ApprovalInput } from './ApprovalCommand.jsx'

const sizes = ['scrollHeight', 'clientHeight']
afterEach(() => {
  cleanup()
  for (const k of sizes) delete HTMLPreElement.prototype[k]
})

const box = () => screen.getByLabelText(en.ask.commandLabel)
const lines = n => Array.from({ length: n }, (_, i) => `echo ${i}`).join('\n')
// jsdom lays nothing out: give the command box the sizes a browser would.
const sized = (scrollHeight, clientHeight) => {
  Object.defineProperty(HTMLPreElement.prototype, 'scrollHeight', { configurable: true, get: () => scrollHeight })
  Object.defineProperty(HTMLPreElement.prototype, 'clientHeight', { configurable: true, get: () => clientHeight })
}

describe('invisible characters in an approval command', () => {
  const hidden = [
    0x00, 0x07, 0x0d, 0x1b, 0x7f, 0x80, 0x9f, 0xad, 0x61c,
    0x200b, 0x200c, 0x200d, 0x200e, 0x200f, 0x2028, 0x2029,
    0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2060, 0x2066, 0x2067, 0x2068, 0x2069, 0xfeff,
  ]
  it.each(hidden)('shows code point %i as an escape', code => {
    render(<ApprovalCommand command={`rm ${String.fromCodePoint(code)}x`} />)
    const hex = code.toString(16).toUpperCase().padStart(4, '0')
    expect(box().textContent).toBe(`rm \\u${hex}x`)
    expect(screen.getByTestId('approval-escape')).toHaveTextContent(`\\u${hex}`)
  })

  it('escapes a character beyond four hex digits', () => {
    render(<ApprovalCommand command={`ls${String.fromCodePoint(0xe0041)}`} />)
    expect(box().textContent).toBe('ls\\u{E0041}')
  })

  it('leaves ordinary text, tabs and line breaks alone', () => {
    const command = 'echo "día 東京 🚀"\n\tls -a'
    render(<ApprovalCommand command={command} />)
    expect(box().textContent).toBe(command)
    expect(screen.queryByTestId('approval-escape')).toBeNull()
  })

  it('escapes them in the role folder too', () => {
    render(<ApprovalCommand command="ls" cwd={`/w/${String.fromCodePoint(0x202e)}gpj`} />)
    expect(screen.getByTestId('approval-command')).toHaveTextContent('Role folder: /w/\\u202Egpj')
  })
})

describe('hidden text in an approval command', () => {
  it('warns that it may include further commands', () => {
    render(<ApprovalCommand command="API_KEY=[REDACTED] ./deploy" />)
    expect(screen.getByTestId('approval-command')).toHaveTextContent(en.ask.commandRedacted)
  })

  it('does not warn otherwise', () => {
    render(<ApprovalCommand command="./deploy" />)
    expect(screen.getByTestId('approval-command')).not.toHaveTextContent(en.ask.commandRedacted)
  })
})

describe('a command that does not fit its box', () => {
  it('says there is more below until the end is reached', () => {
    sized(400, 160)
    render(<ApprovalCommand command="one very long line" />)
    expect(screen.getByText(en.ask.commandMore)).toBeInTheDocument()
    box().scrollTop = 240
    fireEvent.scroll(box())
    expect(screen.queryByText(en.ask.commandMore)).toBeNull()
    box().scrollTop = 0
    fireEvent.scroll(box())
    expect(screen.getByText(en.ask.commandMore)).toBeInTheDocument()
  })

  it('says nothing when it all shows', () => {
    sized(160, 160)
    render(<ApprovalCommand command={lines(3)} />)
    expect(screen.queryByText(en.ask.commandMore)).toBeNull()
  })

  it('counts the lines while the box has no size yet', () => {
    render(<ApprovalCommand command={lines(30)} />)
    expect(screen.getByText(en.ask.commandMore)).toBeInTheDocument()
    cleanup()
    render(<ApprovalCommand command={lines(3)} />)
    expect(screen.queryByText(en.ask.commandMore)).toBeNull()
  })
})

describe('every argument of a call', () => {
  const shown = () => screen.getByTestId('approval-input')

  it('shows the command and every other argument beside it', () => {
    const input = { command: 'git push', dangerouslyDisableSandbox: true, force: false, target: 'prod' }
    render(<ApprovalCall command="git push" input={input} />)
    expect(box().textContent).toBe('git push')
    expect(JSON.parse(shown().textContent)).toEqual({ dangerouslyDisableSandbox: true, force: false, target: 'prod' })
  })

  it('shows all of them for a call with no command, and no box when the command is all there is', () => {
    render(<ApprovalCall input={{ command: ['rm', '-rf'], url: 'u' }} />)
    expect(JSON.parse(shown().textContent)).toEqual({ command: ['rm', '-rf'], url: 'u' })
    cleanup()
    render(<ApprovalCall command="ls" input={{ command: 'ls' }} />)
    expect(screen.queryByTestId('approval-input')).toBeNull()
  })

  it('keeps an input command that is not the one in the command box', () => {
    render(<ApprovalCall command="ls" input={{ command: 'rm x' }} />)
    expect(JSON.parse(shown().textContent)).toEqual({ command: 'rm x' })
  })

  it('says there is more below until the end is reached', () => {
    sized(400, 160)
    render(<ApprovalInput input={{ a: 1 }} />)
    expect(screen.getByText(en.ask.inputMore)).toBeInTheDocument()
    const el = screen.getByLabelText(en.ask.inputLabel)
    el.scrollTop = 240
    fireEvent.scroll(el)
    expect(screen.queryByText(en.ask.inputMore)).toBeNull()
  })

  it('counts the lines while the box has no size yet', () => {
    render(<ApprovalInput input={Object.fromEntries(Array.from({ length: 20 }, (_, i) => [`k${i}`, i]))} />)
    expect(screen.getByText(en.ask.inputMore)).toBeInTheDocument()
    cleanup()
    render(<ApprovalInput input={{ a: 1 }} />)
    expect(screen.queryByText(en.ask.inputMore)).toBeNull()
  })

  it('warns when monomind cut or hid part of an argument', () => {
    // monomind keeps the first 200 UTF-16 units of a string argument, then adds an ellipsis
    render(<ApprovalInput input={{ content: `${'a'.repeat(200)}\u2026`, token: '[REDACTED]' }} />)
    expect(screen.getByText(en.ask.inputCut)).toBeInTheDocument()
    expect(screen.getByText(en.ask.inputRedacted)).toBeInTheDocument()
    cleanup()
    render(<ApprovalInput input={{ content: 'wait\u2026' }} />)
    expect(screen.queryByText(en.ask.inputCut)).toBeNull()
    expect(screen.queryByText(en.ask.inputRedacted)).toBeNull()
  })
})

describe('the warnings', () => {
  it('exist in both locales', () => {
    for (const key of ['commandRedacted', 'commandMore', 'inputLabel', 'inputMore', 'inputCut', 'inputRedacted']) {
      expect(en.ask[key]).toBeTruthy()
      expect(es.ask[key]).toBeTruthy()
      expect(es.ask[key]).not.toBe(en.ask[key])
    }
  })
})

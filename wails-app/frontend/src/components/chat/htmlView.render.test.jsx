// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, act } from '@testing-library/react'

vi.mock('../../services/api.js', () => ({ api: { openURL: vi.fn(), readWorkspaceImage: vi.fn() } }))

import { SandboxedHtmlFrame } from './SandboxedHtmlFrame.jsx'
import { ChatMarkdown } from './ChatMarkdown.jsx'
import { HtmlViewContext } from './htmlView.jsx'
import {
  buildSrcdoc, validateFrameMessage, createHostGate, createFocusGesture, htmlTooLarge, MAX_HTML_BYTES, FLAG_KEY, GESTURE_TTL_MS,
} from './htmlViewContract.js'

afterEach(() => { cleanup(); localStorage.clear() })

function openFrame(props = {}) {
  const p = { html: '<b>hi</b>', onSend: vi.fn(), onAppLink: vi.fn(), ...props }
  const r = render(<SandboxedHtmlFrame {...p} />)
  fireEvent.click(screen.getByText('Show interactive view'))
  return { p, iframe: r.container.querySelector('iframe') }
}

function post(iframe, data, source = iframe.contentWindow) {
  act(() => { window.dispatchEvent(new MessageEvent('message', { data, source })) })
}
// A user click in the frame: the iframe becomes active, the host window blurs.
const hover = (iframe) => {
  iframe.focus()
  act(() => { window.dispatchEvent(new Event('blur')) })
}

describe('iframe lock-down', () => {
  it('collapsed by default: source only, badge visible, no iframe', () => {
    const { container } = render(<SandboxedHtmlFrame html="<i>x</i>" />)
    expect(container.querySelector('iframe')).toBeNull()
    expect(screen.getByTestId('html-view-source')).toHaveTextContent('<i>x</i>')
    expect(screen.getByTestId('html-view-badge')).toHaveTextContent('sandboxed, no network')
  })

  it('sandbox is exactly allow-scripts', () => {
    const { iframe } = openFrame()
    expect(iframe.getAttribute('sandbox')).toBe('allow-scripts')
    for (const bad of ['allow-same-origin', 'allow-top-navigation', 'allow-forms', 'allow-popups']) {
      expect(iframe.getAttribute('sandbox')).not.toContain(bad)
    }
  })

  it('srcdoc starts with the CSP meta before any script or model markup', () => {
    const { iframe } = openFrame({ html: '<script>evil()</script>' })
    const doc = iframe.getAttribute('srcdoc')
    const csp = doc.indexOf('Content-Security-Policy')
    expect(csp).toBeGreaterThan(0)
    expect(csp).toBeLessThan(doc.indexOf('<script>'))
    expect(csp).toBeLessThan(doc.indexOf('evil()'))
    expect(doc).toContain("default-src 'none'")
    expect(doc).toContain("connect-src 'none'")
    expect(doc).toContain("form-action 'none'")
    expect(doc).toContain("frame-src 'none'")
    expect(buildSrcdoc('')).toMatch(/^<!doctype html><html><head><meta http-equiv="Content-Security-Policy"/)
  })

  it('oversize html falls back to source with no toggle', () => {
    const big = 'a'.repeat(MAX_HTML_BYTES + 1)
    expect(htmlTooLarge(big)).toBe(true)
    const { container } = render(<SandboxedHtmlFrame html={big} />)
    expect(screen.getByTestId('html-view-toolarge')).toBeInTheDocument()
    expect(screen.queryByText('Show interactive view')).toBeNull()
    expect(container.querySelector('iframe')).toBeNull()
  })
})

describe('message validation', () => {
  it('accepts well-formed messages and clamps height', () => {
    expect(validateFrameMessage({ v: 1, t: 'resize', h: 5 })).toEqual({ t: 'resize', h: 40 })
    expect(validateFrameMessage({ v: 1, t: 'resize', h: 99999 })).toEqual({ t: 'resize', h: 600 })
    expect(validateFrameMessage({ v: 1, t: 'open', href: 'monoagent://task/12' })).toEqual({ t: 'open', href: 'monoagent://task/12' })
  })
  it('rejects wrong shape, extra keys, oversize, bad links', () => {
    const bad = [
      null, 'x', 5, [], { t: 'send', text: 'hi' }, { v: 2, t: 'send', text: 'hi' },
      { v: 1, t: 'send', text: 'hi', extra: 1 }, { v: 1, t: 'send', text: 5 }, { v: 1, t: 'send', text: '   ' },
      { v: 1, t: 'send', text: 'a'.repeat(4001) }, { v: 1, t: 'resize', h: '90' }, { v: 1, t: 'resize', h: NaN },
      { v: 1, t: 'open', href: 'https://evil.example/' }, { v: 1, t: 'open', href: 'javascript:alert(1)' },
      { v: 1, t: 'open', href: 'monoagent://task/../x y' }, { v: 1, t: '__proto__' }, { v: 1, t: 'toString' },
      Object.create({ v: 1, t: 'gesture' }),
    ]
    for (const b of bad) expect(validateFrameMessage(b)).toBeNull()
  })
})

describe('host gate', () => {
  const fw = {}
  const mk = (extra = {}) => {
    let t = 1000
    const gate = createHostGate({ getFrameWindow: () => fw, now: () => t, ...extra })
    return { gate, tick: (ms) => { t += ms } }
  }
  const ev = (data, source = fw) => ({ data, source })

  it('ignores messages from any other source', () => {
    const { gate } = mk()
    gate(ev({ v: 1, t: 'gesture' }))
    expect(gate(ev({ v: 1, t: 'send', text: 'hi' }, {}))).toBeNull()
    expect(gate(ev({ v: 1, t: 'send', text: 'hi' }, null))).toBeNull()
  })
  it('refuses send/open without a gesture, and replays after one is used', () => {
    const { gate } = mk()
    expect(gate(ev({ v: 1, t: 'send', text: 'hi' }))).toBeNull()
    gate(ev({ v: 1, t: 'gesture' }))
    expect(gate(ev({ v: 1, t: 'send', text: 'hi' }))).toEqual({ t: 'send', text: 'hi' })
    expect(gate(ev({ v: 1, t: 'send', text: 'hi' }))).toBeNull()
  })
  it('refuses a stale gesture', () => {
    const { gate, tick } = mk()
    gate(ev({ v: 1, t: 'gesture' }))
    tick(GESTURE_TTL_MS + 1)
    expect(gate(ev({ v: 1, t: 'open', href: 'monoagent://task/1' }))).toBeNull()
  })
  it('a gesture the host did not see (pointer elsewhere) does not count', () => {
    const { gate } = mk({ pointerOver: () => false })
    gate(ev({ v: 1, t: 'gesture' }))
    expect(gate(ev({ v: 1, t: 'send', text: 'hi' }))).toBeNull()
  })
  it('rate-limits floods', () => {
    const { gate } = mk()
    let ok = 0
    for (let i = 0; i < 100; i++) if (gate(ev({ v: 1, t: 'resize', h: 100 }))) ok++
    expect(ok).toBe(30)
  })
})

describe('focus gesture', () => {
  it('blur while the frame is active enables one gesture', () => {
    let t = 1000
    const frame = {}
    const g = createFocusGesture({ getFrame: () => frame, getActive: () => frame, now: () => t })
    g.onBlur()
    expect(g.take()).toBe(true)
    expect(g.take()).toBe(false)
  })
  it('blur with another active element does not', () => {
    const g = createFocusGesture({ getFrame: () => ({}), getActive: () => ({}) })
    g.onBlur()
    expect(g.take()).toBe(false)
  })
  it('expires', () => {
    let t = 1000
    const frame = {}
    const g = createFocusGesture({ getFrame: () => frame, getActive: () => frame, now: () => t })
    g.onBlur(); t += GESTURE_TTL_MS + 1
    expect(g.take()).toBe(false)
  })
})

describe('component wiring', () => {
  it('a spoofed in-frame gesture with no host-seen blur never sends', () => {
    const { p, iframe } = openFrame()
    post(iframe, { v: 1, t: 'gesture' })
    post(iframe, { v: 1, t: 'send', text: 'spoof' })
    expect(p.onSend).not.toHaveBeenCalled()
  })
  it('blur with another element active does not enable a send', () => {
    const { p, iframe } = openFrame()
    document.body.focus()
    act(() => { window.dispatchEvent(new Event('blur')) })
    post(iframe, { v: 1, t: 'gesture' })
    post(iframe, { v: 1, t: 'send', text: 'x' })
    expect(p.onSend).not.toHaveBeenCalled()
  })
  it('one blur backs one send only', () => {
    const { p, iframe } = openFrame()
    hover(iframe)
    post(iframe, { v: 1, t: 'gesture' })
    post(iframe, { v: 1, t: 'send', text: 'a' })
    post(iframe, { v: 1, t: 'gesture' })
    post(iframe, { v: 1, t: 'send', text: 'b' })
    expect(p.onSend).toHaveBeenCalledTimes(1)
  })
  it('resizes, and delivers send/open only after a gesture', () => {
    const { p, iframe } = openFrame()
    post(iframe, { v: 1, t: 'resize', h: 250 })
    expect(iframe.style.height).toBe('250px')
    post(iframe, { v: 1, t: 'send', text: 'no gesture' })
    expect(p.onSend).not.toHaveBeenCalled()
    hover(iframe)
    post(iframe, { v: 1, t: 'gesture' })
    post(iframe, { v: 1, t: 'send', text: 'go' })
    expect(p.onSend).toHaveBeenCalledWith('go')
    hover(iframe)
    post(iframe, { v: 1, t: 'gesture' })
    post(iframe, { v: 1, t: 'open', href: 'monoagent://task/9' })
    expect(p.onAppLink).toHaveBeenCalledWith('monoagent://task/9')
  })
  it('ignores a message from another window and no-ops without callbacks', () => {
    const { p, iframe } = openFrame({ onSend: undefined, onAppLink: undefined })
    hover(iframe)
    post(iframe, { v: 1, t: 'gesture' })
    post(iframe, { v: 1, t: 'send', text: 'x' }, window)
    post(iframe, { v: 1, t: 'gesture' })
    expect(() => post(iframe, { v: 1, t: 'send', text: 'x' })).not.toThrow()
    expect(p.onSend).toBeUndefined()
  })
})

describe('ChatMarkdown hook-in', () => {
  const md = '```html-view\n<b>hi</b>\n```'
  it('flag off (default): plain code block, no html view', () => {
    const { container } = render(<ChatMarkdown content={md} />)
    expect(screen.queryByTestId('html-view')).toBeNull()
    expect(container.querySelector('pre')).toHaveTextContent('<b>hi</b>')
  })
  it('flag on: renders the collapsed view and passes context callbacks', () => {
    localStorage.setItem(FLAG_KEY, 'true')
    const onSend = vi.fn()
    const { container } = render(<HtmlViewContext.Provider value={{ onSend }}><ChatMarkdown content={md} /></HtmlViewContext.Provider>)
    expect(screen.getByTestId('html-view')).toBeInTheDocument()
    expect(screen.getByTestId('html-view-source')).toHaveTextContent('<b>hi</b>')
    expect(container.querySelector('iframe')).toBeNull()
  })
  it('flag on does not affect ordinary code blocks', () => {
    localStorage.setItem(FLAG_KEY, 'true')
    render(<ChatMarkdown content={'```html\n<b>x</b>\n```'} />)
    expect(screen.queryByTestId('html-view')).toBeNull()
  })
  it('only the exact string true enables it', () => {
    localStorage.setItem(FLAG_KEY, '1')
    render(<ChatMarkdown content={md} />)
    expect(screen.queryByTestId('html-view')).toBeNull()
  })
})

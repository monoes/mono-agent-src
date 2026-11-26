// @vitest-environment jsdom
// A person must see all of what a card's button would send: every text field
// shows its whole value and is as tall as its text.
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, act, cleanup } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { A2UISurface } from './A2UISurface.jsx'
import '../../i18n.js'

// jsdom lays nothing out: a text box here is `width` characters wide, 20px a line.
let width
const lines = el => el.value.split('\n').reduce((n, l) => n + Math.max(1, Math.ceil(l.length / width)), 0)
const sizes = {
  scrollHeight() { return this.tagName === 'TEXTAREA' && width ? Math.max(lines(this), this.rows) * 20 : 0 },
  offsetHeight() { return 22 },
  clientHeight() { return this.tagName === 'TEXTAREA' ? 20 : 0 },
  clientWidth() { return width },
}
const observers = []
const frames = []
beforeEach(() => {
  width = 50
  observers.length = 0
  frames.length = 0
  for (const [name, get] of Object.entries(sizes)) Object.defineProperty(HTMLTextAreaElement.prototype, name, { configurable: true, get })
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback) { this.callback = callback; this.on = true; observers.push(this) }
    observe() {}
    disconnect() { this.on = false }
  })
  vi.stubGlobal('requestAnimationFrame', fn => frames.push(fn))
  vi.stubGlobal('cancelAnimationFrame', () => {})
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  for (const name of Object.keys(sizes)) delete HTMLTextAreaElement.prototype[name]
})

const surface = (value, variant, extra = {}) => ({
  surfaceId: 's', provenance: 'system', status: 'live', lastSeq: 1, dataModel: { v: value },
  components: { root: { id: 'root', component: 'TextField', label: 'Message', value: { '@path': '/v' }, ...(variant ? { variant } : {}) } },
  ...extra,
})
const box = () => screen.getByLabelText('Message')
const tall = n => `${n * 20 + 3}px` // the lines, the 2px border and 1px for rounding

describe('A2UI text fields show their whole text', () => {
  it.each([['longText'], ['shortText'], [undefined]])('%s is a wrapping box that holds the whole value', variant => {
    const value = `${'word '.repeat(4000)}THE END: $499`
    const onAction = vi.fn()
    render(<A2UISurface surface={surface(value, variant)} onAction={onAction} />)
    expect(box().tagName).toBe('TEXTAREA')
    expect(box().value).toBe(value)
    expect(box().maxLength).toBeGreaterThanOrEqual(value.length)
    expect(box().style.height).toBe(tall(lines(box())))
  })

  it('grows on an edit, on a data update, on a width change and when a font loads', () => {
    const fonts = new EventTarget()
    Object.defineProperty(document, 'fonts', { configurable: true, value: fonts })
    const s = surface('x'.repeat(200), 'longText')
    const { rerender, unmount } = render(<A2UISurface surface={s} />)
    expect(box().style.height).toBe(tall(4))
    fireEvent.change(box(), { target: { value: 'y'.repeat(500) } })
    expect(box().style.height).toBe(tall(10))
    rerender(<A2UISurface surface={{ ...s, dataModel: { v: 'z'.repeat(1000) }, lastSeq: 2 }} />)
    expect(box().style.height).toBe(tall(20))
    width = 20
    act(() => { observers.at(-1).callback([]); frames.splice(0).forEach(f => f()) })
    expect(box().style.height).toBe(tall(50))
    // The same width again (our own resize reports it) measures nothing.
    act(() => observers.at(-1).callback([]))
    expect(frames).toHaveLength(0)
    box().style.height = '1px'
    act(() => { fonts.dispatchEvent(new Event('loadingdone')); frames.splice(0).forEach(f => f()) })
    expect(box().style.height).toBe(tall(50))
    unmount()
    expect(observers.at(-1).on).toBe(false)
    delete document.fonts
  })

  it('is measured once a hidden card is shown', () => {
    width = 0
    render(<A2UISurface surface={surface('x'.repeat(300), 'longText')} />)
    expect(box().style.height).toBe('auto')
    width = 100
    act(() => { observers.at(-1).callback([]); frames.splice(0).forEach(f => f()) })
    expect(box().style.height).toBe(tall(3))
  })

  it('a one-line field takes no line break from the keyboard; long text and number fields are unchanged', () => {
    render(<A2UISurface surface={surface('a', 'shortText')} />)
    expect(box().rows).toBe(1)
    expect(fireEvent.keyDown(box(), { key: 'Enter' })).toBe(false)
    cleanup()
    render(<A2UISurface surface={surface('a', 'longText')} />)
    expect(box().rows).toBe(3)
    expect(fireEvent.keyDown(box(), { key: 'Enter' })).toBe(true)
    cleanup()
    render(<A2UISurface surface={surface(5, 'number')} />)
    expect(box().tagName).toBe('INPUT')
    expect(box().type).toBe('number')
  })

  it('no style rule hides or cuts a card’s text', () => {
    const css = readFileSync(join(__dirname, 'a2ui.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
    const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(m => [m[1].trim(), m[2]])
    expect(rules.length).toBeGreaterThan(20)
    // .a2ui-sr is for screen readers only; the progress bar holds no text.
    for (const [selector, body] of rules) {
      if (selector === '.a2ui-sr' || selector.startsWith('.a2ui-bar')) continue
      expect(body, selector).not.toMatch(/line-clamp|text-overflow|nowrap|max-height|overflow(-[xy])?:\s*(hidden|clip)|resize:\s*(vertical|both)/)
    }
  })
})

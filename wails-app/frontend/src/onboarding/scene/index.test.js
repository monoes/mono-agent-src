// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { prefersReducedMotion } from './motion.js'
import { createScene, BUILD_FOR_STEP, STEP_IDS } from './index.js'

vi.mock('./motion.js', () => ({ prefersReducedMotion: vi.fn(() => false) }))

const statusesUpTo = (n, extra = {}) =>
  Object.assign(Object.fromEntries(STEP_IDS.map((id, i) => [id, i < n ? 'done' : 'todo'])), extra)
const click = el => el.dispatchEvent(new MouseEvent('click', { bubbles: true }))

let host, scene
beforeEach(() => {
  prefersReducedMotion.mockReturnValue(false)
  host = document.createElement('div')
  document.body.appendChild(host)
})
afterEach(() => {
  if (scene) scene.destroy()
  scene = null
  document.body.innerHTML = ''
  vi.useRealTimers()
})

function mountScene(opts) {
  scene = createScene(opts)
  scene.mount(host)
  return host.querySelector('.onb-scene')
}

describe('createScene', () => {
  it('exports the build for each named step', () => {
    expect(BUILD_FOR_STEP).toEqual({ profile: 'house', monomind: 'windmill', agents: 'robots', browser: 'bridge', firstRun: 'campfire' })
  })

  it('mount adds one aria-hidden root with no focusable element', () => {
    const root = mountScene()
    scene.setState(statusesUpTo(9), 'finish')
    expect(host.querySelectorAll('.onb-scene')).toHaveLength(1)
    expect(root.getAttribute('aria-hidden')).toBe('true')
    expect(root.querySelectorAll('[tabindex], a, button, input, select, textarea')).toHaveLength(0)
  })

  it('setState marks each plot with its status', () => {
    const root = mountScene()
    scene.setState(statusesUpTo(3), 'about')
    expect(root.querySelector('[data-plot="house"][data-status="done"]')).not.toBeNull()
    expect(root.querySelector('[data-plot="house"]').classList.contains('built')).toBe(true)
    expect(root.querySelector('[data-plot="garden"][data-status="todo"]')).not.toBeNull()
    expect(root.querySelector('[data-plot="garden"]').classList.contains('plan')).toBe(true)
    expect(root.querySelector('[data-plot="windmill"]').classList.contains('built')).toBe(false)
  })

  it('finished shows the whole village built, with no plan, no tag and no sunrise', () => {
    const root = mountScene()
    scene.setState(statusesUpTo(3, { about: 'skipped' }), 'about', { finished: true })
    const plots = [...root.querySelectorAll('.onb-vsvg .plot')]
    expect(plots.length).toBeGreaterThan(5)
    for (const g of plots) {
      expect(g.classList.contains('built')).toBe(true)
      expect(g.classList.contains('plan')).toBe(false)
      expect(g.classList.contains('ghost')).toBe(false)
    }
    expect(root.querySelector('.onb-plantag').getAttribute('opacity')).toBe('0')
    expect(root.classList.contains('dawn')).toBe(false)
    // and back to the normal flow without the flag
    scene.setState(statusesUpTo(3), 'about')
    expect(root.querySelector('[data-plot="windmill"]').classList.contains('built')).toBe(false)
  })

  it('a skipped step leaves an outline', () => {
    const root = mountScene()
    scene.setState(statusesUpTo(3, { about: 'skipped' }), 'monomind')
    const garden = root.querySelector('[data-plot="garden"]')
    expect(garden.getAttribute('data-status')).toBe('skipped')
    expect(garden.classList.contains('ghost')).toBe(true)
    expect(garden.classList.contains('built')).toBe(false)
    scene.setState(statusesUpTo(5), 'agents')
    expect(garden.classList.contains('built')).toBe(true)
  })

  it('playBuild resolves and leaves the plot built', async () => {
    vi.useFakeTimers()
    const root = mountScene()
    scene.setState(statusesUpTo(3), 'about')
    let done = false
    const p = scene.playBuild('profile').then(() => { done = true })
    const house = root.querySelector('[data-plot="house"]')
    expect(house.classList.contains('built')).toBe(false)
    expect(root.querySelector('.onb-fx .scaf')).not.toBeNull()
    await vi.advanceTimersByTimeAsync(2450)
    await p
    expect(done).toBe(true)
    expect(house.classList.contains('built')).toBe(true)
    expect(root.querySelector('.onb-fx .scaf')).toBeNull()
  })

  it('under reduced motion playBuild resolves immediately and adds no animation class', async () => {
    vi.useFakeTimers()
    prefersReducedMotion.mockReturnValue(true)
    const root = mountScene()
    expect(root.classList.contains('onb-motion')).toBe(false)
    scene.setState(statusesUpTo(5), 'agents')
    let done = false
    scene.playBuild('profile').then(() => { done = true })
    await Promise.resolve()
    expect(done).toBe(true)
    expect(root.querySelector('[data-plot="house"]').classList.contains('built')).toBe(true)
    expect(root.querySelector('.onb-fx .scaf')).toBeNull()
    expect(vi.getTimerCount()).toBe(0)
    click(root.querySelector('[data-hit="windmill"]'))
    expect(root.querySelector('.onb-found').hidden).toBe(false)
  })

  it('a discovery click raises the counter and shows it only after the first find', () => {
    const root = mountScene({ labels: { found: (n, total) => `Found ${n} of ${total}` } })
    expect(root.classList.contains('onb-motion')).toBe(true)
    scene.setState(statusesUpTo(5), 'agents')
    const found = root.querySelector('.onb-found')
    expect(found.hidden).toBe(true)
    click(root.querySelector('[data-hit="windmill"]'))
    expect(found.hidden).toBe(false)
    expect(found.textContent).toBe('Found 1 of 12')
    click(root.querySelector('[data-hit="windmill"]'))
    expect(found.textContent).toBe('Found 1 of 12')
    click(root.querySelector('[data-hit="tree"]'))
    expect(found.textContent).toBe('Found 2 of 12')
    expect(found.querySelector('b').textContent).toBe('2')
  })

  it('bolds the number of finds even when the translation puts the total first', () => {
    const root = mountScene({ labels: { found: (n, total) => `${total} hidden, ${n} found` } })
    scene.setState(statusesUpTo(5), 'agents')
    click(root.querySelector('[data-hit="windmill"]'))
    const found = root.querySelector('.onb-found')
    expect(found.textContent).toBe('12 hidden, 1 found')
    expect(found.querySelector('b').textContent).toBe('1')
    expect(found.firstChild.textContent).toBe('12 hidden, ')
  })

  it('labels the plot the current step builds, and shows no tag without a label', () => {
    let root = mountScene({ labels: { plan: id => `plan ${id}` } })
    scene.setState(statusesUpTo(2), 'profile')
    const tag = () => root.querySelector('.onb-plantag')
    expect(tag().textContent).toBe('plan profile')
    expect(tag().getAttribute('opacity')).toBe('1')
    scene.destroy()
    root = mountScene()
    scene.setState(statusesUpTo(2), 'profile')
    expect(tag().getAttribute('opacity')).toBe('0')
  })

  it('a hit inside a plot that is not built does nothing', () => {
    const root = mountScene()
    scene.setState(statusesUpTo(3), 'about')
    click(root.querySelector('[data-hit="windmill"]'))
    expect(root.querySelector('.onb-found').hidden).toBe(true)
  })

  it('clicking the scene does not move focus from an input', () => {
    const root = mountScene()
    scene.setState(statusesUpTo(5), 'agents')
    const input = document.createElement('input')
    document.body.appendChild(input)
    input.focus()
    const target = root.querySelector('[data-hit="windmill"]')
    const down = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    target.dispatchEvent(down)
    click(target)
    click(root.querySelector('[data-hit="sky"]'))
    // jsdom never moves focus on a dispatched mouse event, so the real check is the
    // prevented mousedown: that is what keeps focus in the form in a browser.
    expect(down.defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(input)
    const key = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    root.dispatchEvent(key)
    expect(key.defaultPrevented).toBe(false)
  })

  it('destroy removes the root and every listener', () => {
    vi.useFakeTimers()
    const root = mountScene()
    scene.setState(statusesUpTo(5), 'agents')
    const mill = root.querySelector('[data-hit="windmill"]')
    const found = root.querySelector('.onb-found')
    scene.playBuild('monomind')
    const off = vi.spyOn(window, 'removeEventListener')
    scene.destroy()
    scene = null
    expect(off.mock.calls.some(([type]) => type === 'resize')).toBe(true)
    off.mockRestore()
    expect(host.querySelector('.onb-scene')).toBeNull()
    expect(vi.getTimerCount()).toBe(0)
    click(mill)
    expect(found.hidden).toBe(true)
  })

  it('lighting warms with each built plot and the sun rises on the last step', () => {
    const root = mountScene()
    scene.setState(statusesUpTo(2), 'profile')
    const warm = () => +root.querySelector('.onb-warmv').getAttribute('opacity')
    const low = warm()
    scene.setState(statusesUpTo(6), 'browser')
    expect(warm()).toBeGreaterThan(low)
    expect(root.classList.contains('dawn')).toBe(false)
    scene.setState(statusesUpTo(9), 'finish')
    expect(root.classList.contains('dawn')).toBe(true)
    expect(root.querySelector('[data-plot="village"]').classList.contains('built')).toBe(true)
  })
})

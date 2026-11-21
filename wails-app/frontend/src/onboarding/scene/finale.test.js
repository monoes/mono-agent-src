// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { prefersReducedMotion } from './motion.js'
import { createScene, STEP_IDS } from './index.js'

vi.mock('./motion.js', () => ({ prefersReducedMotion: vi.fn(() => false) }))

const ALL_DONE = Object.fromEntries(STEP_IDS.map(id => [id, id === 'finish' ? 'todo' : 'done']))
const click = el => el.dispatchEvent(new MouseEvent('click', { bubbles: true }))

let host, scene, root, onRampageEnd
beforeEach(() => {
  vi.useFakeTimers()
  prefersReducedMotion.mockReturnValue(false)
  host = document.createElement('div')
  document.body.appendChild(host)
  onRampageEnd = vi.fn()
  scene = createScene({ onRampageEnd })
  scene.mount(host)
  root = host.querySelector('.onb-scene')
})
afterEach(() => {
  if (scene) scene.destroy()
  scene = null
  document.body.innerHTML = ''
  vi.useRealTimers()
})

const hit = k => root.querySelector(`[data-hit="${k}"]`)
const mill = () => click(hit('windmill'))
const boosted = () => root.querySelector('.onb-vsvg').classList.contains('boost')
const raging = () => root.classList.contains('ramp')
function combination({ mills = 7, between = null, landFirst = false } = {}) {
  for (let k = 0; k < mills; k++) mill()
  if (between) between()
  click(hit('fire'))
  click(hit('drone'))
  if (landFirst) vi.advanceTimersByTime(7000)
  mill()
}

describe('the last step', () => {
  it('a windmill click on the last step sets boost and clears it after 3000 ms; a second click restarts it', () => {
    scene.setState(ALL_DONE, 'finish')
    mill()
    expect(boosted()).toBe(true)
    vi.advanceTimersByTime(2000)
    mill()
    vi.advanceTimersByTime(2000)
    expect(boosted()).toBe(true)
    vi.advanceTimersByTime(1001)
    expect(boosted()).toBe(false)
  })

  it('a windmill click before the last step does not set boost', () => {
    scene.setState(Object.assign({}, ALL_DONE, { firstRun: 'todo' }), 'firstRun')
    mill()
    expect(boosted()).toBe(false)
  })

  it('a drone click does nothing when finish is done but another step is current', () => {
    scene.setState(Object.assign({}, ALL_DONE, { finish: 'done' }), 'browser')
    const drone = hit('drone')
    expect(drone.classList.contains('on')).toBe(true)
    click(drone)
    expect(drone.classList.contains('dup')).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('a resize in the ruin moves the Rebuild control with the window', () => {
    scene.setState(ALL_DONE, 'finish')
    combination()
    vi.advanceTimersByTime(17000)
    expect(onRampageEnd).toHaveBeenCalledTimes(1)
    window.dispatchEvent(new Event('resize'))
    expect(onRampageEnd).toHaveBeenCalledTimes(2)
    scene.rebuild()
    window.dispatchEvent(new Event('resize'))
    expect(onRampageEnd).toHaveBeenCalledTimes(2)
  })

  it('a drone click lifts the drone and it lands after about 6000 ms', () => {
    scene.setState(ALL_DONE, 'finish')
    const drone = hit('drone')
    click(drone)
    expect(drone.classList.contains('dup')).toBe(true)
    vi.advanceTimersByTime(6000 + 900)
    expect(drone.classList.contains('dup')).toBe(false)
  })

  it('the exact combination starts the rampage', () => {
    scene.setState(ALL_DONE, 'finish')
    combination()
    expect(raging()).toBe(true)
    expect(boosted()).toBe(false)
  })

  it('more than seven windmill clicks also work', () => {
    scene.setState(ALL_DONE, 'finish')
    combination({ mills: 10 })
    expect(raging()).toBe(true)
  })

  it('a different scene click in between, six windmill clicks, or a landed drone do not start it', () => {
    scene.setState(ALL_DONE, 'finish')
    combination({ between: () => click(hit('sky')) })
    expect(raging()).toBe(false)
    click(hit('sky'))
    combination({ mills: 6 })
    expect(raging()).toBe(false)
    click(hit('sky'))
    combination({ landFirst: true })
    expect(raging()).toBe(false)
  })

  it('the combination before the last step does nothing', () => {
    scene.setState(Object.assign({}, ALL_DONE, { firstRun: 'todo' }), 'firstRun')
    combination()
    expect(raging()).toBe(false)
  })

  it('the rampage ends in a ruin and calls onRampageEnd once', () => {
    scene.setState(ALL_DONE, 'finish')
    combination()
    vi.advanceTimersByTime(10000)
    expect(onRampageEnd).not.toHaveBeenCalled()
    vi.advanceTimersByTime(7000)
    expect(root.classList.contains('ruin')).toBe(true)
    expect(onRampageEnd).toHaveBeenCalledTimes(1)
    expect(onRampageEnd.mock.calls[0][0]).toEqual({ left: expect.any(Number), top: expect.any(Number) })
  })

  it('under reduced motion the rampage jumps to the ruin', () => {
    scene.destroy()
    prefersReducedMotion.mockReturnValue(true)
    scene = createScene({ onRampageEnd })
    scene.mount(host)
    root = host.querySelector('.onb-scene')
    scene.setState(ALL_DONE, 'finish')
    combination()
    expect(root.classList.contains('ruin')).toBe(true)
    expect(onRampageEnd).toHaveBeenCalledTimes(1)
  })

  it('scene clicks during the rampage do nothing', () => {
    scene.setState(ALL_DONE, 'finish')
    combination()
    const before = root.querySelector('.onb-found').textContent
    click(hit('tree'))
    click(hit('flowers'))
    expect(root.querySelector('.onb-found').textContent).toBe(before)
  })

  it('rebuild restores the village', () => {
    scene.setState(ALL_DONE, 'finish')
    combination()
    vi.advanceTimersByTime(17000)
    scene.rebuild()
    expect(raging()).toBe(false)
    expect(root.classList.contains('ruin')).toBe(false)
    expect(root.querySelector('[data-plot="house"]').classList.contains('built')).toBe(true)
    expect(root.querySelector('[data-plot="village"]').classList.contains('built')).toBe(true)
    expect(hit('drone').classList.contains('on')).toBe(true)
  })

  it('leaving the last step stops the rampage', () => {
    scene.setState(ALL_DONE, 'finish')
    combination()
    scene.setState(ALL_DONE, 'firstRun')
    expect(raging()).toBe(false)
  })

  it('destroy during a rampage leaves no timers running', () => {
    scene.setState(ALL_DONE, 'finish')
    combination()
    vi.advanceTimersByTime(3000)
    scene.destroy()
    scene = null
    expect(vi.getTimerCount()).toBe(0)
    expect(onRampageEnd).not.toHaveBeenCalled()
  })
})

// The night village that grows behind the onboarding panel: one plot per step,
// built with a short transition when the step is really done, inhabitants that
// arrive in waves, hidden things to click, lighting that warms with progress,
// a sunrise on the last step, and the last step's windmill boost, drone and
// secret rampage. Ported from the approved mockup
// docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.
//
// Framework-free: createScene() -> { mount(el), setState(statuses, currentStep, { finished }?),
// playBuild(stepId): Promise<void>, rebuild(), destroy() }. The root is
// aria-hidden, holds nothing focusable, and never touches keyboard events.
import './scene.css'
import './scene-motion.css'
import './scene-rampage.css'
import { prefersReducedMotion } from './motion.js'
import { SKY_SVG } from './art-sky.js'
import { VILLAGE_DEFS, GROUND_SVG } from './art-ground.js'
import { PLOT_SVG, LIT_WINDOW_FILLS } from './art-buildings.js'
import { DRONE_SVG } from './art-robots.js'
import { ACTORS_SVG } from './inhabitants.js'
import { JAR, createDiscoveries } from './discoveries.js'
import { createBuilds } from './builds.js'
import { createFinale } from './finale.js'
import { createRampage } from './rampage.js'

export const STEP_IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']

/* the builds the plan names; every step builds something, see PLOT_FOR_STEP */
export const BUILD_FOR_STEP = { profile: 'house', monomind: 'windmill', agents: 'robots', browser: 'bridge', firstRun: 'campfire' }

export const PLOT_FOR_STEP = {
  welcome: 'path', system: 'workshop', profile: 'house', about: 'garden', monomind: 'windmill',
  agents: 'robots', browser: 'bridge', permissions: 'gate', firstRun: 'campfire', finish: 'village',
}
const STEP_FOR_PLOT = Object.fromEntries(Object.entries(PLOT_FOR_STEP).map(([s, p]) => [p, s]))

/* where each plot sits, for the plan tag, the build spotlight and the Rebuild control */
export const PLOT_POS = {
  path: { x: 150, y: 612 }, workshop: { x: 178, y: 470 }, house: { x: 355, y: 418 }, garden: { x: 355, y: 582 },
  windmill: { x: 520, y: 344 }, robots: { x: 300, y: 650 }, bridge: { x: 770, y: 468 }, gate: { x: 600, y: 548 },
  campfire: { x: 515, y: 630 }, village: { x: 800, y: 418 },
}
/* drawing order, back to front */
const PLOT_ORDER = ['village', 'workshop', 'windmill', 'house', 'bridge', 'gate', 'garden', 'path', 'campfire', 'robots']

const DEFAULT_LABELS = { found: (n, total) => `${n} / ${total}`, plan: () => '' }

export function createScene(options = {}) {
  const labels = Object.assign({}, DEFAULT_LABELS, options.labels)
  const onRampageEnd = options.onRampageEnd || (() => {})
  let root = null, alive = false
  const timers = new Set(), frames = new Set(), offs = []
  const state = { statuses: {}, current: null, finished: false }

  const ctx = {
    rm: false, labels, state, onRampageEnd, PLOT_POS,
    q: s => root.querySelector(s),
    qa: s => Array.from(root.querySelectorAll(s)),
    get root() { return root },
    isLast: () => state.current === 'finish',
    later(ms, fn) {
      if (!alive) return 0
      const t = setTimeout(() => { timers.delete(t); fn() }, ms)
      timers.add(t)
      return t
    },
    cancel(t) { if (t) { clearTimeout(t); timers.delete(t) } },
    frame(fn) {
      if (!alive) return 0
      const raf = typeof requestAnimationFrame === 'function'
      const box = {}
      const run = () => { frames.delete(box); fn(performance.now()) }
      box.cancel = raf ? (id => () => cancelAnimationFrame(id))(requestAnimationFrame(run)) : (id => () => clearTimeout(id))(setTimeout(run, 16))
      frames.add(box)
      return box
    },
    cancelFrame(box) { if (box && box.cancel) { box.cancel(); frames.delete(box) } },
    anim: (el, kf, o) => (ctx.rm || !el || !el.animate) ? null : el.animate(kf, o),
    addFx(html, host) {
      host = host || ctx.q('.onb-fx')
      const g = document.createElementNS(host.namespaceURI, 'g')
      g.innerHTML = html
      host.appendChild(g)
      return g
    },
    plotEl: name => ctx.q(`.onb-vsvg .plot[data-plot="${name}"]`),
    render: () => render(),
    buildVillage: () => buildVillage(),
  }
  ctx.builds = createBuilds(ctx)
  ctx.finds = createDiscoveries(ctx)
  ctx.finale = createFinale(ctx)
  ctx.ramp = createRampage(ctx)

  function buildVillage() {
    const v = ctx.q('.onb-vsvg')
    v.innerHTML = `${VILLAGE_DEFS}
    <g class="onb-vcam">${GROUND_SVG()}
    <ellipse class="onb-warmv" cx="500" cy="600" rx="460" ry="200" fill="url(#onb-v-warm)" opacity="0"/>
    ${PLOT_ORDER.map(p => `<g class="plot" data-plot="${p}" data-status="todo">${PLOT_SVG[p]()}</g>`).join('')}
    ${ACTORS_SVG()}${JAR}<g class="onb-ramp"></g>${DRONE_SVG}<g class="onb-fx"></g></g>
    <g class="onb-plantag" opacity="0"><rect x="0" y="0" height="28" rx="14" stroke-width="1.2"/><text x="14" y="18.5"></text></g>`
    LIT_WINDOW_FILLS.forEach(f => v.querySelectorAll(`rect[fill="${f}"]`).forEach(e => e.classList.add('lw')))
  }

  /* the left edge of the free view, right of the panel, in village coordinates */
  function minX() {
    try {
      const svg = ctx.q('.onb-vsvg'), m = svg.getScreenCTM().inverse(), pt = svg.createSVGPoint()
      pt.x = ctx.q('.onb-village').getBoundingClientRect().left + 100; pt.y = 0
      return Math.max(4, pt.matrixTransform(m).x)
    } catch { return 4 }
  }

  function placeTag() {
    const tag = ctx.q('.onb-plantag'), id = state.current, p = PLOT_POS[PLOT_FOR_STEP[id]]
    const txt = p && !state.finished ? labels.plan(id) || '' : ''
    tag.querySelector('text').textContent = txt
    if (!txt) { tag.setAttribute('opacity', '0'); return }
    let w = 220
    try { w = tag.querySelector('text').getComputedTextLength() + 28 } catch { /* no layout */ }
    tag.querySelector('rect').setAttribute('width', w.toFixed(0))
    const x = Math.max(minX(), Math.min(896 - w, p.x - w / 2)), y = p.y - 34
    tag.style.transform = `translate(${x}px, ${y}px)`
    tag.setAttribute('opacity', '1')
  }

  /* where the Rebuild control goes: over the last plot, in px from the root's top left */
  ctx.rebuildAnchor = () => {
    try {
      const svg = ctx.q('.onb-vsvg'), m = svg.getScreenCTM(), pt = svg.createSVGPoint(), q = PLOT_POS.village
      const half = 80, lo = minX() + half
      pt.x = Math.max(lo, Math.min(896 - half, q.x)); pt.y = q.y - 34
      const s = pt.matrixTransform(m), r = root.getBoundingClientRect()
      return { left: Math.round(s.x - r.left), top: Math.round(s.y - r.top) }
    } catch { return { left: 0, top: 0 } }
  }

  function render() {
    if (!root) return
    const { statuses, current, finished } = state, last = current === 'finish'
    if (!last) { ctx.ramp.stop(); ctx.finale.leave() }
    const building = ctx.builds.building, built = new Set()
    ctx.qa('.onb-vsvg .plot').forEach(g => {
      const plot = g.dataset.plot, step = STEP_FOR_PLOT[plot], st = statuses[step] || 'todo'
      /* finished: the whole village stands, whatever the statuses say */
      const isB = (finished || st === 'done' || (plot === 'village' && last)) && !building.has(plot)
      const plan = !isB && (step === current || building.has(plot))
      g.classList.toggle('built', isB)
      g.classList.toggle('plan', plan)
      g.classList.toggle('ghost', !isB && !plan && st === 'skipped')
      g.setAttribute('data-status', st)
      if (isB) built.add(plot)
    })
    ctx.qa('.onb-actors .actor, .onb-drone').forEach(a => a.classList.toggle('on', built.has(a.dataset.need)))
    ctx.q('.onb-warmv').setAttribute('opacity', (built.size * 0.09).toFixed(2))
    ctx.q('.onb-warmsky').setAttribute('opacity', (built.size * 0.06).toFixed(2))
    root.classList.toggle('dawn', !finished && (last || statuses.finish === 'done'))
    placeTag()
    ctx.finds.refresh()
  }

  function onMouseDown(e) {
    /* scene clicks never move focus away from the form */
    if (e.target.closest && e.target.closest('.onb-village, .onb-world')) e.preventDefault()
  }
  function onClick(e) {
    if (!e.target.closest || !e.target.closest('.onb-village, .onb-world')) return
    let h = e.target.closest('[data-hit]')
    /* what is not built or has not arrived cannot be clicked (CSS does this in a browser) */
    if (h) { const p = h.closest('.plot, .actor'); if (p && !p.classList.contains(p.classList.contains('plot') ? 'built' : 'on')) h = null }
    const k = h ? h.dataset.hit : ''
    if (ctx.ramp.active() || ctx.finale.secret(k)) return
    if (k === 'windmill' && ctx.isLast()) ctx.finale.boost()
    /* the drone, like the boost and the combination, belongs to the last step only */
    if (k === 'drone') { if (ctx.isLast()) ctx.finale.droneFly(); return }
    const f = h && ctx.finds.HIT[k]
    if (f) f(h, e)
  }
  /* the plan tag and, in the ruin, the Rebuild control follow the window */
  const onResize = () => {
    placeTag()
    if (root.classList.contains('ruin')) onRampageEnd(ctx.rebuildAnchor())
  }

  function listen(target, type, fn) { target.addEventListener(type, fn); offs.push(() => target.removeEventListener(type, fn)) }

  return {
    mount(el) {
      if (root) return
      alive = true
      ctx.rm = !!prefersReducedMotion()
      root = document.createElement('div')
      root.className = 'onb-scene' + (ctx.rm ? '' : ' onb-motion')
      root.setAttribute('aria-hidden', 'true')
      root.innerHTML = `${SKY_SVG()}<div class="onb-village"><svg class="onb-vsvg" viewBox="0 0 900 700" preserveAspectRatio="xMidYMax meet"></svg></div><div class="onb-vignette"></div><div class="onb-alarm"></div><div class="onb-found" hidden></div>`
      buildVillage()
      el.appendChild(root)
      listen(root, 'mousedown', onMouseDown)
      listen(root, 'click', onClick)
      listen(window, 'resize', onResize)
      render()
    },
    /* opts.finished: the finished village (all plots built, no sunrise, no
       plan tag), for the short setup of a later profile */
    setState(statuses, currentStep, opts) {
      state.statuses = Object.assign({}, statuses)
      state.current = currentStep
      state.finished = !!(opts && opts.finished)
      render()
    },
    playBuild(stepId) {
      const plot = PLOT_FOR_STEP[stepId]
      if (!root || !plot) return Promise.resolve()
      return ctx.builds.play(plot)
    },
    rebuild() {
      if (!root) return
      ctx.ramp.stop()
      render()
    },
    destroy() {
      if (!root) return
      ctx.finds.stopFollow()
      ctx.builds.reset()
      alive = false
      timers.forEach(clearTimeout); timers.clear()
      frames.forEach(b => b.cancel()); frames.clear()
      offs.splice(0).forEach(off => off())
      root.remove()
      root = null
    },
  }
}

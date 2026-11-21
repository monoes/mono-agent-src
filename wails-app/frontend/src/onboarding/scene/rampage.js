// The rampage the secret combination sets off, about 16.5 s, cartoon style:
// red alert, the robots' eyes turn red and they chase the humans down, the
// drone lasers the rest, the houses collapse and burn one after another; it
// ends in a smouldering ruin and calls onRampageEnd so React can show the
// Rebuild control. The originals are hidden and stand-ins are animated in
// .onb-ramp; stop() regenerates the whole village. Under reduced motion it
// jumps straight to the ruin.
// Ported from docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.
import { WALL, WALL2, ROOF, WOOD, DARK } from './art-ground.js'
import { walkFig } from './art-figures.js'

const DPX = 474, DPY = 497 /* the drone's feet when it hovers over its pad */
const RW = [
  { at: 2.6, plot: 'house', wall: 0, roof: 1, det: [2], fx: 1, cx: 355, base: 576, w: 110, h: 146 },
  { at: 4.4, plot: 'workshop', wall: 0, roof: 1, det: [2], cx: 180, base: 554, w: 84, h: 80 },
  { at: 6.2, plot: 'bridge', whole: [1], cx: 791, base: 586, w: 80, h: 102 },
  { at: 8.0, plot: 'village', whole: [0], cx: 771, base: 472, w: 50, h: 58 },
  { at: 9.4, plot: 'village', whole: [1], cx: 850, base: 478, w: 52, h: 54 },
  { at: 11.0, plot: 'windmill', mill: 1, cx: 520, base: 552, w: 40, h: 120 },
]
const END_MS = 16500
const rand = (a, b) => a + Math.random() * (b - a)
const clamp = (v, a, b) => Math.max(a, Math.min(b, v))
const nearest = (p, list) => { let best = null, bd = 1e9; for (const q of list) { const d = Math.hypot(q.x - p.x, q.y - p.y); if (d < bd) { bd = d; best = q } } return best }

function rfHtml(f) {
  let inner = walkFig(f.kind, f.c, false)
  if (f.kind === 'robot') inner = inner.replace('fill="oklch(0.86 0.12 165)"/>', 'class="eye" fill="oklch(0.86 0.12 165)"/>') + `<circle class="eg" cx="0" cy="-21" r="6" fill="url(#onb-v-red)"/>`
  return `<g class="rf${f.kind === 'robot' ? ' robo' : ''} still"><g class="rfd"><g class="rfr"><g class="bob"><g transform="scale(1.25)">${inner}</g></g>${f.kind === 'human' ? '<text class="ex" x="0" y="-37" text-anchor="middle">!</text>' : ''}</g></g></g>`
}

export function createRampage(ctx) {
  let RAMP = null
  const vsvg = () => ctx.q('.onb-vsvg')
  const anim = (el, kf, o) => ctx.anim(el, kf, o)
  const addFx = (html, host) => ctx.addFx(html, host)

  function rlater(ms, fn) { const R = RAMP; if (ctx.rm) return fn(); R.tm.push(ctx.later(ms, () => { if (RAMP === R) fn() })) }
  /* animate to an end state that stays; under reduced motion jump straight to it */
  function settle(el, kf, o) { if (anim(el, kf, Object.assign({ fill: 'forwards' }, o))) return; const e = kf[kf.length - 1]; for (const p in e) if (p !== 'offset' && p !== 'easing') el.style[p] = e[p] }
  function drawFig(f) {
    f.el.setAttribute('transform', `translate(${f.x.toFixed(1)} ${f.y.toFixed(1)})`)
    if (f.fd !== f.face) { f.fd = f.face; f.el.firstElementChild.setAttribute('transform', f.face < 0 ? 'scale(-1 1)' : '') }
    if (f.rn !== f.run) { f.rn = f.run; f.el.classList.toggle('still', !f.run) }
  }
  function drawDrone() {
    const R = RAMP, d = ctx.q('.onb-drone')
    d.querySelector('.dmove').setAttribute('transform', `translate(${(R.D.x - DPX).toFixed(1)} ${(R.D.y - DPY).toFixed(1)})`)
    d.querySelector('.dtilt').setAttribute('transform', `rotate(${(R.tilt || 0).toFixed(1)} 0 -10)`)
  }
  function fallDown(f, dir) {
    f.down = true; f.run = false; f.el.classList.remove('alarmed')
    const d = dir * f.face, r = f.el.querySelector('.rfr')
    if (!anim(r, [{ transform: 'rotate(0deg)' }, { transform: `translate(${d * 2}px,-6px) rotate(${d * 28}deg)`, offset: .35 }, { transform: `translateY(-3px) rotate(${d * 90}deg)` }], { duration: 480, easing: 'ease-in', fill: 'forwards' })) r.setAttribute('transform', `translate(0 -3) rotate(${d * 90})`)
  }
  function knock(r, h) {
    const dir = h.x >= r.x ? 1 : -1
    fallDown(h, dir); r.tg = null; r.stun = 1.5; r.run = false
    anim(r.el.querySelector('.rfr'), [{ transform: 'translateX(0px)' }, { transform: 'translateX(5px)' }, { transform: 'translateX(0px)' }], { duration: 260 })
    const g = addFx(`<g transform="translate(${((r.x + h.x) / 2).toFixed(0)} ${(h.y - 26).toFixed(0)})"><path class="pow" d="M0 -8 L2.2 -2.6 L8 -2.4 L3.4 1.2 L5.2 7 L0 3.6 L-5.2 7 L-3.4 1.2 L-8 -2.4 L-2.2 -2.6Z" fill="oklch(0.93 0.16 95)" stroke="oklch(0.35 0.06 40)" stroke-width=".8"/></g>`, ctx.q('.onb-ramp .rt'))
    anim(g.querySelector('.pow'), [{ transform: 'scale(.2) rotate(0deg)', opacity: 1 }, { transform: 'scale(1.5) rotate(20deg)', opacity: 1, offset: .4 }, { transform: 'scale(1.2) rotate(30deg)', opacity: 0 }], { duration: 520, fill: 'forwards' })
    rlater(560, () => g.remove())
  }
  /* the drone marks a human with a red dot, moves over, and fires */
  function zap(h) {
    const R = RAMP; h.zap = true; h.run = false
    R.DT = { x: clamp(h.x + (h.x > 450 ? -80 : 80), 90, 860), y: clamp(h.y - 150, 470, 520) }
    const dot = addFx(`<circle cx="0" cy="-22" r="2.2" fill="oklch(0.68 0.27 28)"/>`, h.el.querySelector('.rfr'))
    rlater(550, () => {
      dot.remove(); if (h.down) return
      const sx = R.D.x, sy = R.D.y - 3, tx = h.x, ty = h.y - 20, L = `x1="${sx.toFixed(0)}" y1="${sy.toFixed(0)}" x2="${tx.toFixed(0)}" y2="${ty.toFixed(0)}" stroke-linecap="round"`
      const g = addFx(`<line ${L} stroke="oklch(0.62 0.26 28)" stroke-width="6" opacity=".45"/><line ${L} stroke="oklch(0.95 0.09 40)" stroke-width="1.8"/><circle cx="${tx.toFixed(0)}" cy="${ty.toFixed(0)}" r="16" fill="url(#onb-v-red)"/><circle cx="${tx.toFixed(0)}" cy="${ty.toFixed(0)}" r="4.5" fill="oklch(0.98 0.05 70)"/>`, ctx.q('.onb-ramp .rt'))
      anim(g, [{ opacity: 1 }, { opacity: 1, offset: .5 }, { opacity: 0 }], { duration: 420, fill: 'forwards' })
      rlater(460, () => g.remove())
      fallDown(h, tx >= sx ? 1 : -1)
    })
  }
  function flames(host, x, y, s) {
    const g = addFx(`<g transform="translate(${x.toFixed(0)} ${y.toFixed(0)}) scale(${s.toFixed(2)})"><g class="rfl"><path class="flick" d="M0 0 c-16 -10 -10 -30 0 -44 c3 14 16 18 12 34 c-2 7 -7 10 -12 10z" fill="oklch(0.70 0.19 42)"/><path class="flick b" d="M0 0 c-9 -6 -6 -18 0 -27 c2 9 9 11 7 20 c-1 4 -4 7 -7 7z" fill="oklch(0.90 0.15 85)"/></g></g>`, host)
    anim(g.querySelector('.rfl'), [{ transform: 'scale(0)' }, { transform: 'scale(1)' }], { duration: rand(500, 900), easing: 'cubic-bezier(.2,.9,.3,1.2)' })
  }
  function smoke(host, x, y, n) {
    let s = ''
    for (let k = 0; k < n; k++) s += `<circle class="rsmoke" style="--sx:${rand(-8, 40).toFixed(0)}px;--st:${rand(3.8, 5.6).toFixed(1)}s;--sd:${(-rand(0, 5)).toFixed(1)}s" cx="${(x + rand(-10, 10)).toFixed(0)}" cy="${(y + rand(-6, 6)).toFixed(0)}" r="${rand(7, 11).toFixed(1)}" fill="oklch(0.30 0.01 290)"/>`
    anim(addFx(s, host), [{ opacity: 0 }, { opacity: 1 }], { duration: 1500 })
  }
  function shake() { anim(ctx.q('.onb-vcam'), [{ transform: 'translate(0px,0px)' }, { transform: 'translate(-3px,2px)' }, { transform: 'translate(3px,-1px)' }, { transform: 'translate(-2px,1px)' }, { transform: 'translate(0px,0px)' }], { duration: 340 }) }
  function debris(host, b) {
    let h = ''; const cols = [WALL, WALL2, ROOF, WOOD, DARK]
    for (let k = 0; k < 10; k++) {
      const sx = b.cx + rand(-b.w / 3, b.w / 3), sy = b.base - rand(10, b.h * .6)
      h += `<g transform="translate(${sx.toFixed(0)} ${sy.toFixed(0)})"><rect class="db" data-dx="${rand(-70, 70).toFixed(0)}" data-ly="${(b.base + rand(-3, 9) - sy).toFixed(0)}" data-r="${rand(-300, 300).toFixed(0)}" x="-3" y="-2" width="${rand(4, 9).toFixed(1)}" height="${rand(3, 5).toFixed(1)}" fill="${cols[k % 5]}"/></g>`
    }
    addFx(h, host).querySelectorAll('.db').forEach(d => {
      const dx = +d.dataset.dx, ly = +d.dataset.ly, r = +d.dataset.r
      if (!anim(d, [{ transform: 'translate(0px,0px) rotate(0deg)', easing: 'ease-out' }, { transform: `translate(${dx / 2}px,${-rand(25, 55).toFixed(0)}px) rotate(${r / 2}deg)`, easing: 'ease-in', offset: .4 }, { transform: `translate(${dx}px,${ly}px) rotate(${r}deg)` }], { duration: rand(800, 1100), fill: 'forwards' })) d.setAttribute('transform', `translate(${dx} ${ly}) rotate(${r})`)
    })
    if (ctx.rm) return
    let s = ''
    for (let k = 0; k < 6; k++) s += `<g transform="translate(${(b.cx + rand(-b.w / 2, b.w / 2)).toFixed(0)} ${(b.base - rand(0, 12)).toFixed(0)})"><circle class="du" r="${rand(7, 12).toFixed(0)}" fill="oklch(0.55 0.02 60)"/></g>`
    const g = addFx(s, host)
    g.querySelectorAll('.du').forEach(c => anim(c, [{ transform: 'translate(0px,0px) scale(.6)', opacity: .7 }, { transform: `translate(${rand(-25, 25).toFixed(0)}px,${-rand(10, 30).toFixed(0)}px) scale(2.6)`, opacity: 0 }], { duration: rand(900, 1400), easing: 'ease-out', fill: 'forwards' }))
    rlater(1500, () => g.remove())
  }
  /* one building breaks apart, then burns */
  function wreck(b) {
    const p = ctx.plotEl(b.plot)
    if (!p || !p.classList.contains('built')) return
    const pcs = p.querySelectorAll(':scope > .pc'), host = ctx.q('.onb-ramp .rb'), CH = 'brightness(.36) saturate(.55)'
    const org = (el, x, y) => { el.style.transformBox = 'view-box'; el.style.transformOrigin = `${x}px ${y}px`; return el }
    const fall = { duration: 750, easing: 'cubic-bezier(.6,0,.9,.5)' }
    if (b.mill) {
      [pcs[0], pcs[1]].forEach(el => settle(el, [{ filter: 'none' }, { filter: CH }], { duration: 2600 }))
      addFx(`<circle class="rgl" cx="520" cy="500" r="70" fill="url(#onb-v-fire)"/>`, host);
      [[512, 550, 1], [528, 520, .8], [515, 490, .7], [525, 462, .6]].forEach(([x, y, s], k) => rlater(k * 380, () => flames(host, x, y, s)))
      rlater(900, () => smoke(host, 520, 452, 4))
      rlater(1800, () => {
        const sp = pcs[2].querySelector('.spin'); if (sp) sp.style.animationPlayState = 'paused'
        org(pcs[2], 520, 420)
        settle(pcs[2], [{ transform: 'none', filter: 'none' }, { transform: 'translate(-34px,98px) rotate(-42deg)', filter: CH }], { duration: 900, easing: 'cubic-bezier(.55,0,.9,.45)' })
        rlater(850, () => { shake(); debris(host, { cx: 490, base: 556, w: 60, h: 30 }) })
      })
      return
    }
    shake()
    if (b.wall != null) {
      const wl = pcs[b.wall]
      let hh = b.h * .6
      try { hh = wl.getBBox().height || hh } catch { /* no layout */ }
      org(wl, b.cx, b.base); settle(wl, [{ transform: 'none', filter: 'none' }, { transform: 'translateX(2px) rotate(1.5deg)', offset: .2 }, { transform: 'scaleY(.3) skewX(-7deg)', filter: CH }], fall)
      org(pcs[b.roof], b.cx, b.base - hh); settle(pcs[b.roof], [{ transform: 'none', filter: 'none' }, { transform: `translateY(${(hh * .7).toFixed(0)}px) rotate(8deg) scaleY(.75)`, filter: CH }], fall)
      b.det.forEach(k => settle(pcs[k], [{ opacity: 1 }, { opacity: 0 }], { duration: 250 }))
    }
    (b.whole || []).forEach(k => { org(pcs[k], b.cx, b.base); settle(pcs[k], [{ transform: 'none', filter: 'none' }, { transform: 'translateX(2px) rotate(2deg)', offset: .2 }, { transform: 'scaleY(.32) skewX(8deg)', filter: CH }], fall) })
    if (b.fx) { const fx = p.querySelector(':scope > .fx'); if (fx) settle(fx, [{ opacity: 1 }, { opacity: 0 }], { duration: 200 }) }
    rlater(550, () => {
      const x0 = b.cx - b.w / 2
      let rub = `M${x0 - 6} ${b.base}`
      for (let k = 0; k <= 8; k++) rub += ` L${(x0 - 6 + (b.w + 12) * k / 8).toFixed(0)} ${(b.base - (k % 2 ? rand(6, 14) : rand(2, 6)) * b.w / 80).toFixed(0)}`
      addFx(`<circle class="rgl" cx="${b.cx}" cy="${b.base - 12}" r="${(b.w * .9).toFixed(0)}" fill="url(#onb-v-fire)"/><path d="${rub} L${x0 + b.w + 6} ${b.base}Z" fill="oklch(0.24 0.03 40)"/>`, host)
      debris(host, b)
      const n = Math.max(2, Math.round(b.w / 24))
      for (let k = 0; k < n; k++) rlater(k * 160, () => flames(host, x0 + b.w * (k + .5) / n + rand(-5, 5), b.base - rand(2, b.h * .16), rand(.7, 1.1) * (b.w > 70 ? 1.05 : .75)))
      rlater(600, () => smoke(host, b.cx, b.base - b.h * .3, b.w > 70 ? 5 : 3))
    })
  }
  function tick(now) {
    const R = RAMP; if (!R || R.ended) return
    const dt = Math.min(.05, Math.max(0, (now - R.last) / 1000)), t = (now - R.t0) / 1000; R.last = now
    const hs = R.figs.filter(f => f.kind === 'human' && !f.down), rs = R.figs.filter(f => f.kind === 'robot')
    if (t > 1.3) {
      for (const r of rs) {
        r.run = false
        if (r.stun > 0) { r.stun -= dt; continue }
        if (!r.tg || r.tg.down) { const free = hs.filter(h => !rs.some(o => o !== r && o.tg === h)); r.tg = nearest(r, free.length ? free : hs) }
        const h = r.tg; if (!h) continue
        const dx = h.x - r.x, dy = h.y - r.y, d = Math.hypot(dx, dy) || 1
        if (d < 11) { knock(r, h); continue }
        r.x += dx / d * 44 * dt; r.y += dy / d * 34 * dt; r.face = dx < 0 ? -1 : 1; r.run = true
      }
      for (const h of hs) {
        h.run = false; if (h.zap) continue
        const r = nearest(h, rs); if (!r) continue
        let vx = h.x - r.x, vy = h.y - r.y; const d = Math.hypot(vx, vy) || 1
        if (d > 130) continue
        vx /= d; vy /= d
        if ((h.x < 60 && vx < 0) || (h.x > 870 && vx > 0)) { vx = 0; vy = vy >= 0 ? 1 : -1 }
        h.x = clamp(h.x + vx * 33 * dt, 40, 885); h.y = clamp(h.y + vy * 26 * dt, 566, 698); if (vx) h.face = vx < 0 ? -1 : 1; h.run = true
      }
    }
    if (t >= R.shot) { const c = hs.filter(h => !h.zap), free = c.filter(h => !rs.some(r => r.tg === h)), h = nearest(R.D, free.length ? free : c); if (h) zap(h); R.shot = t + (t > 13.5 ? .45 : 2.3) }
    if (dt) { const k = Math.min(1, dt * 2.4), nx = R.D.x + (R.DT.x - R.D.x) * k; R.tilt = clamp((nx - R.D.x) / dt * .09, -14, 14); R.D.x = nx; R.D.y += (R.DT.y - R.D.y) * k }
    drawDrone(); R.figs.forEach(drawFig)
    R.raf = ctx.frame(tick)
  }
  /* every figure on screen, where it stands now, in village coordinates */
  function collectFigs() {
    const figs = []
    try {
      const inv = ctx.q('.onb-vcam').getScreenCTM().inverse()
      ctx.qa('.onb-vsvg .plot.built [data-fig], .onb-actors .actor.on [data-fig]').forEach(el => {
        if (!el.getBoundingClientRect().width) return
        const m = inv.multiply(el.getScreenCTM())
        figs.push({ kind: el.dataset.fig, c: el.dataset.c.split('|'), x: m.e, y: Math.max(560, m.f), face: 1, run: false })
      })
    } catch { /* no layout: nobody to chase */ }
    return figs.sort((a, b) => a.y - b.y)
  }
  function start() {
    if (RAMP) return
    const figs = collectFigs()
    ctx.finale.boostOff()
    if (!ctx.finale.droneUp) ctx.finale.droneFly()
    ctx.finale.holdDrone()
    const now = performance.now()
    RAMP = { t0: now, last: now, figs, tm: [], raf: null, shot: 2.2, D: { x: DPX, y: DPY }, DT: { x: DPX, y: DPY - 6 }, tilt: 0, ended: false }
    vsvg().classList.add('ramp'); ctx.root.classList.add('ramp')
    ctx.q('.onb-ramp').innerHTML = `<g class="rb"></g><g class="rfg"></g><g class="rt"></g>`
    figs.forEach(f => { f.el = addFx(rfHtml(f), ctx.q('.onb-ramp .rfg')).firstElementChild; f.rn = false; drawFig(f) })
    drawDrone()
    if (ctx.rm) { figs.forEach(f => f.kind === 'robot' && f.el.classList.add('evil')); RW.forEach(wreck); return end() }
    rlater(600, () => figs.forEach(f => { if (f.kind === 'robot') { f.el.classList.add('evil'); anim(f.el.querySelector('.rfr'), [{ transform: 'translateY(0px)' }, { transform: 'translateY(-5px)' }, { transform: 'translateY(0px)' }], { duration: 260 }) } }))
    rlater(900, () => figs.forEach(f => { if (f.kind === 'human' && !f.down) f.el.classList.add('alarmed') }))
    rlater(2300, () => figs.forEach(f => f.el.classList.remove('alarmed')))
    RW.forEach(b => rlater(b.at * 1000, () => wreck(b)))
    rlater(END_MS, end)
    RAMP.raf = ctx.frame(tick)
  }
  function end() {
    const R = RAMP; if (!R || R.ended) return
    R.ended = true; ctx.cancelFrame(R.raf)
    R.figs.forEach((f, k) => {
      if (f.kind === 'human' && !f.down) fallDown(f, k % 2 ? 1 : -1)
      if (f.kind === 'robot') { f.el.classList.add('evil'); f.face = Math.random() < .5 ? -1 : 1 }
      f.run = false; drawFig(f)
    })
    ctx.qa('.onb-ramp .rfl').forEach(el => { if (!anim(el, [{ transform: 'scale(1)' }, { transform: 'scale(.5)' }], { duration: 2500, easing: 'ease-in-out', fill: 'forwards' })) el.setAttribute('transform', 'scale(.5)') })
    R.tilt = 0; drawDrone()
    ctx.root.classList.add('ruin')
    ctx.onRampageEnd(ctx.rebuildAnchor())
  }
  /* stop everything the rampage started and regenerate the village from scratch */
  function stop() {
    const R = RAMP; if (!R) return
    RAMP = null; ctx.cancelFrame(R.raf); R.tm.forEach(t => ctx.cancel(t))
    ctx.root.classList.remove('ramp', 'ruin'); vsvg().classList.remove('ramp')
    ctx.finds.stopFollow()
    ctx.finale.droneReset(); ctx.finale.seqReset()
    ctx.buildVillage()
  }

  return { start, stop, active: () => !!RAMP }
}

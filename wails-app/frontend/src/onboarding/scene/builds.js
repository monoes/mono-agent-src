// The build transition, about 2.45 s: workers carry pieces to the plot, the
// scaffolding goes up, they hammer, the plot rises, everyone cheers under
// confetti while the camera leans in. The panel is never touched. At most two
// run at once; a third, or any under reduced motion, just shows the built plot.
// Ported from docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.
import { walkFig } from './art-figures.js'
import { P_GOLD } from './art-people.js'
import { R_A, R_B } from './art-robots.js'

const rand = (a, b) => a + Math.random() * (b - a)

export function createBuilds(ctx) {
  const building = new Set(), pending = new Set()
  let events = 0

  function confetti(host, x, y) {
    const cols = ['oklch(0.85 0.15 75)', 'oklch(0.78 0.14 20)', 'oklch(0.82 0.13 160)', 'oklch(0.80 0.12 260)', 'oklch(0.95 0.03 90)']
    let h = ''
    for (let k = 0; k < 14; k++) h += `<g transform="translate(${x.toFixed(0)} ${y.toFixed(0)})"><rect class="cf" x="-2" y="-1.2" width="4" height="2.4" fill="${cols[k % 5]}"/></g>`
    ctx.addFx(h, host).querySelectorAll('.cf').forEach((r, k) => {
      const dx = rand(-75, 75), dy = rand(-95, -35), rot = rand(-360, 360)
      ctx.anim(r, [{ transform: 'translate(0px,0px) rotate(0deg)', opacity: 1 }, { transform: `translate(${dx}px,${dy}px) rotate(${rot / 2}deg)`, opacity: 1, offset: .5 }, { transform: `translate(${dx * 1.3}px,${dy + 60}px) rotate(${rot}deg)`, opacity: 0 }], { duration: 950 + k * 20, easing: 'cubic-bezier(.2,.7,.4,1)', fill: 'forwards' })
    })
  }

  function play(n) {
    const plot = ctx.plotEl(n)
    if (ctx.rm || events >= 2 || !plot) { ctx.render(); return Promise.resolve() }
    events++
    building.add(n); if (n === 'campfire') building.add('village')
    ctx.render()
    const P = ctx.PLOT_POS[n]
    let cx = P.x, w = 110, bot = Math.min(694, P.y + 110), h = 90
    try {
      const bb = plot.getBBox()
      if (bb.width) { cx = bb.width > 220 ? P.x : bb.x + bb.width / 2; w = Math.min(Math.max(bb.width, 60), 140); bot = Math.min(694, bb.y + bb.height); h = Math.min(125, Math.max(50, bb.height)) }
    } catch { /* no layout */ }
    const top = bot - h, x0 = cx - w / 2, x1 = cx + w / 2, WD = 'oklch(0.66 0.08 62)'
    let sc = ''
    if (n !== 'path') {
      for (const x of [x0, cx, x1]) sc += `<path d="M${x.toFixed(0)} ${bot.toFixed(0)} V${(top - 6).toFixed(0)}" stroke="${WD}" stroke-width="2.4"/>`
      for (let y = bot - 26; y > top; y -= 28) sc += `<path d="M${(x0 - 4).toFixed(0)} ${y.toFixed(0)} H${(x1 + 4).toFixed(0)}" stroke="oklch(0.58 0.07 55)" stroke-width="3.2"/><path d="M${x0.toFixed(0)} ${(y + 26).toFixed(0)} L${cx.toFixed(0)} ${y.toFixed(0)}" stroke="${WD}" stroke-width="1.2" opacity=".8"/>`
    }
    const kinds = [['robot', R_A], ['human', P_GOLD], ['robot', R_B]]
    const ends = [[x0 - 14, bot + 2], [x1 + 14, bot + 2], [cx, bot + 12]]
    let wk = ''
    ends.forEach(([ex, ey], k) => {
      ey = Math.min(698, ey)
      const sx = Math.max(110, Math.min(880, ex + (k === 1 ? 170 : -170))), sy = Math.min(698, ey + 24)
      wk += `<g transform="translate(${sx.toFixed(0)} ${sy.toFixed(0)})"><g class="wkm" data-dx="${(ex - sx).toFixed(0)}" data-dy="${(ey - sy).toFixed(0)}"><g${ex < sx ? ' transform="scale(-1 1)"' : ''}><g class="wk carry${kinds[k][0] === 'robot' ? ' robo' : ''}"><g class="bob"><g transform="scale(1.25)">${walkFig(kinds[k][0], kinds[k][1], true)}</g></g></g></g></g></g>`
    })
    const cy = (top + bot) / 2
    const g = ctx.addFx(`<circle class="spot" cx="${cx.toFixed(0)}" cy="${cy.toFixed(0)}" r="${(Math.max(w, h) * 1.15).toFixed(0)}" fill="url(#onb-v-glow)" opacity="0"/><g class="scaf">${sc}</g>${wk}`)
    const scaf = g.querySelector('.scaf')
    ctx.anim(g.querySelector('.spot'), [{ opacity: 0 }, { opacity: .95, offset: .3 }, { opacity: .95, offset: .7 }, { opacity: 0 }], { duration: 2400, fill: 'forwards' })
    scaf.style.transformBox = 'fill-box'; scaf.style.transformOrigin = '50% 100%'
    ctx.anim(scaf, [{ transform: 'scaleY(0)', opacity: 0 }, { transform: 'scaleY(1)', opacity: 1 }], { duration: 500, easing: 'cubic-bezier(.2,.9,.3,1)', fill: 'both' })
    g.querySelectorAll('.wkm').forEach((m, k) => ctx.anim(m, [{ transform: 'translate(0px,0px)' }, { transform: `translate(${m.dataset.dx}px,${m.dataset.dy}px)` }], { duration: 820 + k * 90, easing: 'cubic-bezier(.45,0,.3,1)', fill: 'forwards' }))
    const cam = ctx.q('.onb-vcam')
    cam.style.transformOrigin = `${cx.toFixed(0)}px ${cy.toFixed(0)}px`; cam.style.transform = 'scale(1.07)'
    ctx.later(980, () => g.querySelectorAll('.wk').forEach(x => { x.classList.remove('carry'); x.classList.add('hammer') }))
    ctx.later(1450, () => {
      building.delete(n); if (n === 'campfire') building.delete('village')
      ctx.render()
      ctx.anim(scaf, [{ opacity: 1 }, { opacity: 0 }], { duration: 350, fill: 'forwards' })
      g.querySelectorAll('.wk').forEach((x, k) => { x.classList.remove('hammer'); x.classList.add('cheerw'); ctx.anim(x, [{ transform: 'translateY(0px)' }, { transform: 'translateY(-9px)' }, { transform: 'translateY(0px)' }], { duration: 320, iterations: 2, delay: k * 60, easing: 'ease-out' }) })
      confetti(g, cx, top)
      cam.classList.add('cheer'); ctx.later(900, () => cam.classList.remove('cheer'))
    })
    ctx.later(1650, () => { cam.style.transform = '' })
    ctx.later(2050, () => g.querySelectorAll('.wkm').forEach(m => ctx.anim(m.parentNode, [{ opacity: 1 }, { opacity: 0 }], { duration: 350, fill: 'forwards' })))
    return new Promise(resolve => {
      pending.add(resolve)
      ctx.later(2450, () => { g.remove(); events = Math.max(0, events - 1); pending.delete(resolve); resolve() })
    })
  }

  /* destroy: forget what was building and release anyone waiting */
  function reset() {
    building.clear(); events = 0
    pending.forEach(r => r()); pending.clear()
  }

  return { building, play, reset }
}

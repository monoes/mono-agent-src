// The 12 hidden things to click: nine ordinary (windmill, lit window, fire,
// tree, flowers, dancing robot, chatting pair, shooting star, fish) and three
// rarer (a cat after three roof clicks, a drone that follows the pointer from
// the robots' dome, a firefly jar in the grass). A "found N of 12" counter
// appears after the first. Clicks never take focus. Under reduced motion a
// click still counts and gives a still response.
// Ported from docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.
import { FOLLOW_DRONE } from './art-robots.js'

export const FOUND_OF = 12

/* the hidden firefly jar in the grass by the bridge house */
export const JAR = `<g data-hit="jar" transform="translate(792 676)"><rect x="-9" y="-16" width="18" height="18" fill="transparent"/><circle class="jlight" cx="0" cy="-4" r="7" fill="url(#onb-v-ffly)"/><rect x="-3" y="-8" width="6" height="8" rx="1.5" fill="oklch(0.80 0.05 200 / .3)" stroke="oklch(0.85 0.03 200 / .7)" stroke-width=".8"/><rect class="lid" x="-3.4" y="-9.6" width="6.8" height="1.8" rx=".6" fill="oklch(0.55 0.06 55)"/></g>`

const rand = (a, b) => a + Math.random() * (b - a)

/* a client point in an element's own coordinates, or null without layout */
function vpt(e, el) {
  try {
    const svg = el.ownerSVGElement || el, pt = svg.createSVGPoint()
    pt.x = e.clientX; pt.y = e.clientY
    return pt.matrixTransform(el.getScreenCTM().inverse())
  } catch { return null }
}

export function createDiscoveries(ctx) {
  const found = new Set(), coolUntil = {}
  let roofN = 0, follow = null

  /* the counter's number is bold, as in the mockup, wherever the translated sentence puts it:
     the label is asked once with a marker in the number's place, and split there */
  const MARK = '\u0001'
  function showCount() {
    const f = ctx.q('.onb-found'), n = String(found.size)
    const parts = String(ctx.labels.found(MARK, FOUND_OF)).split(MARK)
    f.textContent = ''
    if (parts.length < 2) f.textContent = String(ctx.labels.found(found.size, FOUND_OF))
    else {
      const b = document.createElement('b')
      b.textContent = n
      f.append(parts[0], b, parts.slice(1).join(n))
    }
    f.hidden = false
  }
  function discover(k) {
    if (found.has(k)) return
    found.add(k)
    showCount()
  }
  function cool(k, ms) { const now = Date.now(); if (coolUntil[k] > now) return false; coolUntil[k] = now + ms; return true }

  function startFollow() {
    const cam = ctx.q('.onb-vcam')
    const g = ctx.addFx(FOLLOW_DRONE)
    let x = 215, y = 660, tx = 300, ty = 600, frame = null, end = 0
    const put = () => g.setAttribute('transform', `translate(${x.toFixed(1)} ${y.toFixed(1)}) scale(1.5)`)
    if (ctx.rm) {
      x = 244; y = 648; put()
      end = ctx.later(2500, () => stop())
      const stop = () => { ctx.cancel(end); g.remove(); follow = null }
      follow = stop
      return
    }
    const mv = e => { const p = vpt(e, cam); if (p) { tx = Math.max(120, Math.min(880, p.x)); ty = Math.max(330, Math.min(690, p.y - 20)) } }
    window.addEventListener('pointermove', mv, { passive: true })
    const step = () => { x += (tx - x) * .07; y += (ty - y) * .07; put(); frame = ctx.frame(step) }
    follow = soft => {
      ctx.cancelFrame(frame); ctx.cancel(end); window.removeEventListener('pointermove', mv); follow = null
      if (soft && ctx.anim(g.firstElementChild, [{ transform: 'translate(0px,0px)', opacity: 1 }, { transform: 'translate(40px,-200px)', opacity: 0 }], { duration: 900, fill: 'forwards' })) ctx.later(950, () => g.remove())
      else g.remove()
    }
    end = ctx.later(8000, () => follow && follow(true))
    frame = ctx.frame(step)
  }
  function stopFollow() { if (follow) follow() }

  const HIT = {
    windmill() { if (!cool('windmill', 2600)) return; ctx.anim(ctx.q('.onb-vsvg .sails'), [{ transform: 'rotate(0deg)' }, { transform: 'rotate(1440deg)' }], { duration: 2500, easing: 'cubic-bezier(.15,.8,.3,1)' }); discover('windmill') },
    window() { if (!cool('window', 3600)) return; const p = ctx.plotEl('house'); p.classList.add('lit2'); ctx.later(3500, () => p.classList.remove('lit2')); discover('window') },
    roof() {
      const p = ctx.plotEl('house'); if (p.classList.contains('hascat')) return
      ctx.anim(p, [{ transform: 'translateX(0px)' }, { transform: 'translateX(1.5px)' }, { transform: 'translateX(-1.5px)' }, { transform: 'translateX(0px)' }], { duration: 240 })
      if (++roofN >= 3) { p.classList.add('hascat'); discover('cat') }
    },
    fire() {
      if (!cool('fire', 1600)) return
      const p = ctx.plotEl('campfire'); p.classList.add('flare'); ctx.later(1500, () => p.classList.remove('flare'))
      if (!ctx.rm) {
        let h = ''; for (let k = 0; k < 12; k++) h += `<circle cx="${515 + rand(-6, 6)}" cy="640" r="1.8" fill="oklch(0.92 0.15 80)"/>`
        const g = ctx.addFx(h)
        g.querySelectorAll('circle').forEach(c => ctx.anim(c, [{ transform: 'translate(0px,0px)', opacity: 1 }, { transform: `translate(${rand(-30, 30)}px,${rand(-110, -60)}px)`, opacity: 0 }], { duration: rand(700, 1300), easing: 'cubic-bezier(.2,.7,.4,1)', fill: 'forwards' }))
        ctx.later(1400, () => g.remove())
      }
      discover('fire')
    },
    tree() {
      if (!cool('tree', 1800)) return
      let h = ''
      for (let k = 0; k < 7; k++) h += `<ellipse class="lf" cx="${(262 + rand(-16, 16)).toFixed(1)}" cy="${(552 + rand(-14, 8)).toFixed(1)}" rx="2.6" ry="1.4" fill="oklch(${rand(.5, .72).toFixed(2)} 0.12 ${rand(60, 150).toFixed(0)})"/>`
      h += `<path class="bd" d="M256 546 q3 -3.5 6 0 q3 -3.5 6 0" fill="none" stroke="oklch(0.22 0.05 300)" stroke-width="1.8" stroke-linecap="round"/>`
      const g = ctx.addFx(h)
      g.querySelectorAll('.lf').forEach(l => ctx.anim(l, [{ transform: 'translate(0px,0px) rotate(0deg)', opacity: 1 }, { transform: `translate(${rand(-22, 22)}px,${rand(30, 46)}px) rotate(${rand(-180, 180)}deg)`, opacity: 0 }], { duration: rand(1100, 1700), easing: 'ease-in', fill: 'forwards' }))
      ctx.anim(g.querySelector('.bd'), [{ transform: 'translate(0px,0px)' }, { transform: 'translate(190px,-180px) scale(.7)' }], { duration: 1700, easing: 'cubic-bezier(.4,0,.6,1)', fill: 'forwards' })
      ctx.later(1750, () => g.remove())
      discover('tree')
    },
    flowers(h) { h.classList.add('bloomed'); discover('flowers') },
    dance(h) {
      if (!cool('dance', 2000)) return
      const hop = h.querySelector('.hop'); hop.style.transformBox = 'fill-box'; hop.style.transformOrigin = '50% 100%'
      ctx.anim(hop, [{ transform: 'rotate(0deg) translateY(0px)' }, { transform: 'rotate(-14deg) translateY(-4px)' }, { transform: 'rotate(14deg) translateY(0px)' }, { transform: 'rotate(-14deg) translateY(-4px)' }, { transform: 'rotate(0deg) translateY(0px)' }], { duration: 900, iterations: 2 })
      h.classList.add('dancing'); ctx.later(1800, () => h.classList.remove('dancing'))
      discover('dance')
    },
    chat(h) { if (!cool('chat', 2700)) return; h.classList.add('wb'); ctx.later(2600, () => h.classList.remove('wb')); discover('wave') },
    sky(h, e) {
      if (!cool('sky', 700)) return
      const p = vpt(e, ctx.q('.onb-world'))
      if (p) {
        const g = ctx.addFx(`<g transform="translate(${p.x.toFixed(0)} ${p.y.toFixed(0)})"><g class="ss"><line x1="0" y1="0" x2="80" y2="-34" stroke="url(#onb-starG)" stroke-width="2.4" stroke-linecap="round"/><circle r="2.4" fill="oklch(0.98 0.03 90)"/></g></g>`, ctx.q('.onb-wfx'))
        ctx.anim(g.querySelector('.ss'), [{ transform: 'translate(0px,0px)', opacity: 0 }, { opacity: 1, offset: .15 }, { transform: 'translate(-260px,110px)', opacity: 0 }], { duration: 1100, easing: 'cubic-bezier(.3,.6,.4,1)', fill: 'forwards' })
        ctx.later(1150, () => g.remove())
      }
      discover('star')
    },
    river(h, e) {
      if (!cool('river', 1300)) return
      const p = vpt(e, ctx.q('.onb-vcam'))
      if (p) {
        const g = ctx.addFx(`<g transform="translate(${p.x.toFixed(1)} ${p.y.toFixed(1)})"><circle class="spl" r="3" fill="none" stroke="oklch(0.85 0.06 230)" stroke-width="1.2"/><path class="fish" d="M6 0 q-6 -5 -12 0 q6 5 12 0z M-5 0 l-5 -3.5 v7z" fill="oklch(0.74 0.13 45)"/></g>`)
        const f = g.querySelector('.fish')
        if (!ctx.anim(f, [{ transform: 'translate(0px,0px) rotate(-50deg)' }, { transform: 'translate(10px,-28px) rotate(0deg)', offset: .5 }, { transform: 'translate(20px,2px) rotate(55deg)' }], { duration: 900, easing: 'ease-in-out', fill: 'forwards' })) f.setAttribute('transform', 'translate(10 -20)')
        ctx.anim(g.querySelector('.spl'), [{ transform: 'scale(1)', opacity: 1 }, { transform: 'scale(4)', opacity: 0 }], { duration: 700, fill: 'forwards' })
        ctx.later(1000, () => g.remove())
      }
      discover('fish')
    },
    dome() { if (follow || !cool('dome', 1000)) return; startFollow(); discover('follow') },
    jar(h) {
      if (h.classList.contains('open')) return
      h.classList.add('open')
      let s = ''
      for (let k = 0; k < 9; k++) s += `<circle class="ffly" style="--fx:${rand(-30, 30).toFixed(0)}px;--fy:${rand(-40, -5).toFixed(0)}px;--ft:${rand(2.5, 5).toFixed(1)}s;animation-delay:${(-rand(0, 4)).toFixed(1)}s" cx="${(792 + rand(-26, 26)).toFixed(0)}" cy="${(664 + rand(-40, 6)).toFixed(0)}" r="3.4" fill="url(#onb-v-ffly)"/>`
      ctx.addFx(s)
      discover('jar')
    },
  }

  /* words again, after a language switch */
  function refresh() { if (found.size) showCount() }

  return { HIT, stopFollow, refresh, get count() { return found.size } }
}

// Figures put in place: walkers on a lane, and posed actors that wait for a
// plot to be built. Ported unchanged from
// docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.
import { humanWalk } from './art-people.js'
import { robotWalk } from './art-robots.js'

export const walkFig = (kind, c, carry) => (kind === 'human' ? humanWalk(c, carry) : robotWalk(c, carry))

export function walkerInner(kind, d, dy, t, dl, c, carry) {
  const anim = `--d:${d}px;--dy:${dy}px;--t:${t}s;--dl:${dl}s`
  return `<g class="mover" style="${anim}"><g class="facer" style="${anim}"><g class="hop"><g class="bob"><g transform="scale(1.25)" data-fig="${kind}" data-c="${c.join('|')}">${walkFig(kind, c, carry)}</g></g></g></g></g>`
}

/* walkers: x,y = left end of the lane at the feet; d = lane length; dy = rise over the lane */
export function walker(kind, x, y, d, dy, t, dl, c) {
  return `<g class="fx${kind === 'robot' ? ' robo' : ''}" transform="translate(${x} ${y})">${walkerInner(kind, d, dy, t, dl, c)}</g>`
}

/* an actor appears when the plot named by `need` is built */
export function actor(need, x, y, inner, o = {}) {
  return `<g class="actor${o.cls ? ' ' + o.cls : ''}" data-need="${need}"${o.hit ? ` data-hit="${o.hit}"` : ''} transform="translate(${x} ${y})"><g class="arr"><g class="hop">${o.hit ? `<rect x="${o.hx ?? -16}" y="${o.hy ?? -44}" width="${o.hw ?? 32}" height="${o.hh ?? 48}" fill="transparent"/>` : ''}<g transform="scale(${o.flip ? -1.25 : 1.25} 1.25)">${inner}</g></g></g></g>`
}

export const walkActor = (need, kind, x, y, d, dy, t, dl, c, carry) => `<g class="actor${kind === 'robot' ? ' robo' : ''}${carry ? ' carry' : ''}" data-need="${need}" transform="translate(${x} ${y})"><g class="arr">${walkerInner(kind, d, dy, t, dl, c, carry)}</g></g>`

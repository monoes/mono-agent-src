// The cast that arrives in waves as steps finish, each doing something: a robot
// hammering, a robot carrying planks, two humans chatting, a human watering
// flowers, a robot handing a human a letter, a fisher on the bridge, a human
// and a robot by the fire, a robot waving at the viewer. With the walkers drawn
// into the plots (art-buildings.js) that makes the 20 inhabitants.
// Ported unchanged from docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.
import { man, bubble, HAMMER, CAN, P_BLUE, P_RED, P_GREEN, P_GOLD } from './art-people.js'
import { bot, R_A, R_B } from './art-robots.js'
import { actor, walkActor } from './art-figures.js'

export function ACTORS_SVG() {
  const a = [
    actor('workshop', 238, 561, bot(R_B, { front: { cls: 'a-hammer', st: -100, prop: HAMMER } }), { flip: true }),
    walkActor('house', 'robot', 188, 593, 92, -6, 11, -3, R_A, true),
    actor('house', 360, 668, `${man(P_RED, { front: { cls: 'a-gest' } })}<g transform="translate(21 0) scale(-1 1)">${man(P_GOLD, { front: { cls: 'a-gest d2' } })}</g><g transform="translate(5 -33)">${bubble('bA')}</g><g transform="translate(17 -33)">${bubble('bB')}</g>`, { hit: 'chat', hx: -10, hw: 46, hy: -40, hh: 42 }),
    actor('garden', 350, 633, `${man(P_GREEN, { front: { st: -60, prop: CAN } })}${[0, 1, 2].map(k => `<ellipse class="drop${k ? ' d' + (k + 1) : ''}" cx="${17.4 + k * .8}" cy="-13" rx=".7" ry="1.2" fill="oklch(0.80 0.08 230)"/>`).join('')}`, { flip: true }),
    walkActor('windmill', 'human', 430, 628, 130, -16, 20, -5, P_RED),
    actor('robots', 600, 690, bot(R_A, { face: true, front: { cls: 'a-wave', st: 180 }, back: { st: 12 } }), { hit: 'dance' }),
    actor('robots', 136, 692, `${bot(R_B, { front: { cls: 'a-letter', st: -80 } })}<g transform="translate(30 0) scale(-1 1)">${man(P_BLUE, { front: { cls: 'a-letter', st: -80 } })}</g><rect class="letter" x="12.5" y="-15.5" width="6" height="4.4" rx=".6" fill="oklch(0.96 0.03 85)" stroke="oklch(0.45 0.03 60)" stroke-width=".4"/>`),
    actor('bridge', 664, 605, `${man(P_GOLD, { sit: true, front: { st: -70 } })}<path d="M8 -13.8 L25 -31" stroke="oklch(0.45 0.05 50)" stroke-width="1.1" stroke-linecap="round"/><path d="M25 -31 Q27 -6 24 9" fill="none" stroke="oklch(0.85 0.02 290 / .7)" stroke-width=".5"/><circle class="bobber" cx="24" cy="9" r="1.4" fill="oklch(0.75 0.17 28)"/>`),
    walkActor('gate', 'robot', 744, 652, 110, -14, 16, -8, R_B),
    actor('campfire', 484, 690, man(P_BLUE, { sit: true, front: { cls: 'warm', st: -45 } })),
    actor('campfire', 548, 690, bot(R_A, { sit: true, front: { cls: 'warm', st: -45 } }), { flip: true }),
  ]
  return `<g class="onb-actors">${a.join('')}</g>`
}

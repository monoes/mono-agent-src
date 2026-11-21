// The villagers: a walking human, a posed human, their props and speech
// bubbles. Ported unchanged from
// docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.
import { DARK } from './art-ground.js'

export const P_BLUE = ['oklch(0.55 0.12 255)', 'oklch(0.50 0.12 255)', 'oklch(0.80 0.06 60)', 'oklch(0.30 0.05 40)']
export const P_RED = ['oklch(0.58 0.15 25)', 'oklch(0.52 0.14 25)', 'oklch(0.70 0.08 50)', 'oklch(0.22 0.03 40)']
export const P_GREEN = ['oklch(0.58 0.12 150)', 'oklch(0.52 0.11 150)', 'oklch(0.62 0.07 45)', 'oklch(0.70 0.10 85)']
export const P_GOLD = ['oklch(0.72 0.13 85)', 'oklch(0.64 0.12 80)', 'oklch(0.78 0.06 60)', 'oklch(0.25 0.03 30)']

/* a walking human: feet at 0,0; legs and arms swing from the hip and shoulder */
export const humanWalk = (c, carry) => `<rect class="arm b" x="-1.2" y="-17" width="2.4" height="8" rx="1.2" fill="${c[1]}"/>
      <rect class="leg" x="-1.6" y="-8.5" width="3.2" height="8.5" rx="1.5" fill="oklch(0.28 0.04 270)"/>
      <rect class="leg b" x="-1.6" y="-8.5" width="3.2" height="8.5" rx="1.5" fill="oklch(0.24 0.04 270)"/>
      <rect x="-3.6" y="-18.5" width="7.2" height="11" rx="2.6" fill="${c[0]}"/>
      <circle cx="0" cy="-22.5" r="3.7" fill="${c[2]}"/><path d="M-3.7 -23 a3.7 3.7 0 0 1 7.4 0 l-1 -.2 a3 3 0 0 0 -5 -1z" fill="${c[3]}"/>
      <circle cx="1.8" cy="-22.6" r=".7" fill="oklch(0.2 0.02 50)"/>
      <rect class="arm a" x="-1.2" y="-17" width="2.4" height="8" rx="1.2" fill="${c[0]}"/>${carry ? `<rect class="load" x="-6" y="-33" width="12" height="7" rx="1" fill="oklch(0.62 0.09 55)" stroke="${DARK}" stroke-width=".8"/>` : ''}`

/* posed figures: feet at 0,0, facing right; arms rotate around the shoulder */
export const limb = (x, y, a, col, w, len) => { a = a || {}; return `<g transform="translate(${x} ${y})"><g class="lm ${a.cls || ''}" style="transform:rotate(${a.st ?? 0}deg)"><rect x="${-w / 2}" y="0" width="${w}" height="${len}" rx="${w / 2}" fill="${col}"/>${a.prop || ''}</g></g>` }

export function man(c, o = {}) {
  const P1 = 'oklch(0.28 0.04 270)', P2 = 'oklch(0.24 0.04 270)'
  const legs = o.sit
    ? `<path d="M-1.5 -8.3 H6.5 V-.5" fill="none" stroke="${P2}" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/><path d="M.5 -8 H8.5 V0" fill="none" stroke="${P1}" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/>`
    : `<rect x="-2.9" y="-8.5" width="2.8" height="8.5" rx="1.3" fill="${P2}"/><rect x=".1" y="-8.5" width="2.8" height="8.5" rx="1.3" fill="${P1}"/>`
  return `<g data-fig="human" data-c="${c.join('|')}">` + limb(-.5, -16.5, Object.assign({ st: 8 }, o.back), c[1], 2.4, 8) + legs
    + `<rect x="-3.6" y="-18.5" width="7.2" height="11" rx="2.6" fill="${c[0]}"/><circle cx="0" cy="-22.5" r="3.7" fill="${c[2]}"/><path d="M-3.7 -23 a3.7 3.7 0 0 1 7.4 0 l-1 -.2 a3 3 0 0 0 -5 -1z" fill="${c[3]}"/><circle cx="1.8" cy="-22.6" r=".7" fill="oklch(0.2 0.02 50)"/>`
    + limb(.5, -16.5, Object.assign({ st: -8 }, o.front), c[0], 2.4, 8) + '</g>'
}

export const HAMMER = `<path d="M0 7 v7.5" stroke="oklch(0.45 0.05 50)" stroke-width="1.6"/><rect x="-3.4" y="13.5" width="6.8" height="3.6" rx=".8" fill="oklch(0.74 0.02 260)"/>`
export const CAN = `<g transform="translate(0 8) rotate(60)"><rect x="-1" y="-1" width="7" height="5.5" rx="1.2" fill="oklch(0.62 0.10 200)"/><path d="M6 .5 l5 -2.8" stroke="oklch(0.62 0.10 200)" stroke-width="1.5" stroke-linecap="round"/></g>`
export const bubble = cls => `<g class="bub ${cls}"><rect x="-6" y="-7" width="13" height="7.5" rx="3.7" fill="oklch(0.95 0.01 290)"/><path d="M-2 0 l-2 3 l4 -2.4z" fill="oklch(0.95 0.01 290)"/>${[-2.5, .5, 3.5].map(x => `<circle cx="${x}" cy="-3.2" r=".9" fill="oklch(0.3 0.03 290)"/>`).join('')}</g>`

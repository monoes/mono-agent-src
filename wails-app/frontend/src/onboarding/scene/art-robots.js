// The robots: a walking robot, a posed robot, the parked drone of the finished
// village and the little drone that follows the pointer. Ported unchanged from
// docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.
import { DARK } from './art-ground.js'
import { limb } from './art-people.js'

export const R_A = ['oklch(0.72 0.03 255)', 'oklch(0.83 0.15 72)'], R_B = ['oklch(0.62 0.06 300)', 'oklch(0.78 0.13 25)']

/* a walking robot: feet at 0,0 */
export const robotWalk = (c, carry) => `<rect class="leg" x="-3.6" y="-6.5" width="3" height="6.5" rx="1" fill="oklch(0.50 0.02 260)"/>
      <rect class="leg b" x=".6" y="-6.5" width="3" height="6.5" rx="1" fill="oklch(0.44 0.02 260)"/>
      <rect class="arm a" x="-8" y="-15" width="2.4" height="7" rx="1.2" fill="oklch(0.58 0.02 260)"/>
      <rect class="arm b" x="5.6" y="-15" width="2.4" height="7" rx="1.2" fill="oklch(0.58 0.02 260)"/>
      <rect x="-6" y="-17" width="12" height="11" rx="2.5" fill="${c[0]}"/><circle cx="0" cy="-11.5" r="1.8" fill="var(--accent)" class="blink"/>
      <path d="M0 -24 v-4.5" stroke="oklch(0.6 0.02 260)" stroke-width="1.2"/><circle cx="0" cy="-29" r="1.6" fill="${c[1]}" class="blink"/>
      <rect x="-5" y="-24.5" width="10" height="7.5" rx="2.2" fill="oklch(0.66 0.02 260)"/><rect x="-3.5" y="-22.5" width="7" height="3" rx="1.5" fill="oklch(0.86 0.12 165)"/>${carry ? `<rect class="load" x="-12" y="-28" width="24" height="3.4" rx="1" fill="oklch(0.62 0.09 55)" stroke="${DARK}" stroke-width=".7"/>` : ''}`

export function bot(c, o = {}) {
  const legs = o.sit
    ? `<rect x="-4" y="-8" width="11" height="3" rx="1" fill="oklch(0.50 0.02 260)"/><rect x="5" y="-7" width="3" height="7" rx="1" fill="oklch(0.46 0.02 260)"/>`
    : `<rect x="-3.6" y="-6.5" width="3" height="6.5" rx="1" fill="oklch(0.50 0.02 260)"/><rect x=".6" y="-6.5" width="3" height="6.5" rx="1" fill="oklch(0.44 0.02 260)"/>`
  const eyes = o.face ? `<circle cx="-2.2" cy="-21" r="1.3" fill="oklch(0.86 0.12 165)"/><circle cx="2.2" cy="-21" r="1.3" fill="oklch(0.86 0.12 165)"/>` : `<rect x="-1" y="-22.5" width="5.5" height="3" rx="1.5" fill="oklch(0.86 0.12 165)"/>`
  return `<g data-fig="robot" data-c="${c.join('|')}">` + limb(-7, -15, Object.assign({ st: 10 }, o.back), 'oklch(0.56 0.02 260)', 2.4, 7) + legs
    + `<rect x="-6" y="-17" width="12" height="11" rx="2.5" fill="${c[0]}"/><circle cx="0" cy="-11.5" r="1.8" fill="var(--accent)" class="blink"/><path d="M0 -24 v-4.5" stroke="oklch(0.6 0.02 260)" stroke-width="1.2"/><circle cx="0" cy="-29" r="1.6" fill="${c[1]}" class="blink"/><rect x="-5" y="-24.5" width="10" height="7.5" rx="2.2" fill="oklch(0.66 0.02 260)"/>${eyes}`
    + limb(7, -15, Object.assign({ st: -10 }, o.front), 'oklch(0.64 0.02 260)', 2.4, 7) + '</g>'
}

/* a small drone parked on its pad between the first house and the windmill; it appears with the finished village */
export const DRONE_SVG = `<g class="actor onb-drone" data-need="village" data-hit="drone" transform="translate(474 561)">
  <ellipse cx="0" cy="1.5" rx="15" ry="4.2" fill="oklch(0.27 0.03 265)" stroke="oklch(0.42 0.03 265)" stroke-width=".8"/><path d="M-5 1.5h10" stroke="oklch(0.83 0.15 72 / .55)" stroke-width="1.2"/>
  <circle class="blink" cx="-11.5" cy="1.5" r="1.1" fill="oklch(0.86 0.12 165)"/><circle class="blink" style="animation-delay:-1s" cx="11.5" cy="1.5" r="1.1" fill="oklch(0.86 0.12 165)"/>
  <g class="dmove"><g class="dlift"><rect x="-23" y="-30" width="46" height="34" fill="transparent"/><g class="dtilt"><g class="dhov"><g transform="scale(1.5)">
    <path d="M-5 -3 l-2.4 3 M5 -3 l2.4 3" stroke="oklch(0.52 0.02 260)" stroke-width="1.1" stroke-linecap="round"/>
    <path d="M-11 -12 h22 M-11 -12 v-2.2 M11 -12 v-2.2" stroke="oklch(0.55 0.02 260)" stroke-width="1.2" fill="none"/>
    <path class="dprop" d="M-15 -14.6 h8" stroke="oklch(0.88 0.02 260)" stroke-width="1.4" stroke-linecap="round"/><path class="dprop" d="M7 -14.6 h8" stroke="oklch(0.88 0.02 260)" stroke-width="1.4" stroke-linecap="round"/>
    <rect x="-6" y="-11" width="12" height="8.5" rx="3" fill="oklch(0.70 0.02 260)"/>
    <rect class="dvis" x="-3.5" y="-8.6" width="7" height="2.8" rx="1.4" fill="oklch(0.86 0.12 165)"/>
    <circle cx="0" cy="-1.6" r="1.1" fill="oklch(0.83 0.15 72)"/>
  </g></g></g></g></g></g>`

/* the rare secret of the robots' dome: a little drone that follows the pointer for a while */
export const FOLLOW_DRONE = `<g class="drone"><path class="prop" d="M-8 -15 h16" stroke="oklch(0.88 0.02 260)" stroke-width="1.6" stroke-linecap="round"/><path d="M0 -15 v3" stroke="oklch(0.6 0.02 260)" stroke-width="1.2"/><rect x="-6" y="-12" width="12" height="9" rx="3" fill="oklch(0.70 0.02 260)"/><rect x="-3.5" y="-9.5" width="7" height="3" rx="1.5" fill="oklch(0.86 0.12 165)"/><circle cx="0" cy="2" r="4" fill="url(#onb-v-glow)"/></g>`

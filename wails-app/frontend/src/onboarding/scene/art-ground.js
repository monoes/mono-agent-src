// The village's palette, small drawing helpers, and the ground it stands on
// (hills, pines, footpath, river). Ported unchanged from
// docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.
// The hills and the mask run on to x=2000 so the ground reaches the right edge
// of wide, short windows.

export const LIT = 'oklch(0.88 0.14 80)', WALL = 'oklch(0.40 0.05 45)', WALL2 = 'oklch(0.33 0.045 40)', ROOF = 'oklch(0.40 0.10 28)', DARK = 'oklch(0.20 0.03 40)', WOOD = 'oklch(0.36 0.05 55)'
export const pc = (i, inner) => `<g class="pc" style="--i:${i}">${inner}</g>`
export const glow = (x, y, r, op) => `<circle class="fx glowp" cx="${x}" cy="${y}" r="${r}" fill="url(#onb-v-glow)" opacity="${op || .8}"/>`
export const win = (x, y, w, h, c) => `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="1.5" fill="${c || LIT}" stroke="${DARK}" stroke-width="1.5"/><path d="M${x + w / 2} ${y}v${h}M${x} ${y + h / 2}h${w}" stroke="${DARK}" stroke-width="1"/>`
export const pine = (x, b, h, c) => `<path d="M${x} ${b - h} L${x + h * .32} ${b - h * .15} H${x + 3} V${b} H${x - 3} V${b - h * .15} H${x - h * .32}Z" fill="${c || 'oklch(0.24 0.05 190)'}"/>`
export const tree = (x, b, r, c) => `<path d="M${x - 3} ${b} h6 l-1 ${-r * 1.2} h-4z" fill="${WOOD}"/><circle cx="${x}" cy="${b - r * 1.5}" r="${r}" fill="${c || 'oklch(0.40 0.09 160)'}"/><circle cx="${x - r * .5}" cy="${b - r * 1.2}" r="${r * .7}" fill="${c || 'oklch(0.40 0.09 160)'}"/><circle cx="${x + r * .35}" cy="${b - r * 1.85}" r="${r * .45}" fill="oklch(0.50 0.10 150 / .6)"/>`
export const burst = (x, y) => `<circle class="burst fx" cx="${x}" cy="${y}" r="26" fill="none" stroke="var(--accent)" stroke-width="2"/>`
export const lantern = (x, b, h) => `<path d="M${x} ${b}V${b - h}" stroke="oklch(0.30 0.03 290)" stroke-width="2.5"/><rect x="${x - 4}" y="${b - h - 9}" width="8" height="10" rx="2" fill="${LIT}" stroke="${DARK}"/>${glow(x, b - h - 4, 18, .9)}`

export function bulbs(x1, y1, cx, cy, x2, y2, n) {
  let h = `<path d="M${x1} ${y1} Q${cx} ${cy} ${x2} ${y2}" fill="none" stroke="oklch(0.22 0.03 290)" stroke-width="1"/>`
  const cols = [LIT, 'oklch(0.80 0.13 20)', 'oklch(0.85 0.12 140)', 'oklch(0.83 0.15 72)']
  for (let k = 1; k < n; k++) {
    const t = k / n, x = (1 - t) * (1 - t) * x1 + 2 * (1 - t) * t * cx + t * t * x2, y = (1 - t) * (1 - t) * y1 + 2 * (1 - t) * t * cy + t * t * y2 + 3
    h += `<circle class="bulb" style="animation-delay:${(-k * .37).toFixed(2)}s" cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="2.2" fill="${cols[k % 4]}"/>`
  }
  return h
}

export const VILLAGE_DEFS = `<defs>
      <radialGradient id="onb-v-glow" cx=".5" cy=".5" r=".5"><stop offset="0" stop-color="oklch(0.86 0.15 75)" stop-opacity=".7"/><stop offset="1" stop-color="oklch(0.80 0.15 65)" stop-opacity="0"/></radialGradient>
      <radialGradient id="onb-v-warm" cx=".5" cy=".5" r=".5"><stop offset="0" stop-color="oklch(0.72 0.15 60)" stop-opacity=".5"/><stop offset="1" stop-color="oklch(0.70 0.15 50)" stop-opacity="0"/></radialGradient>
      <radialGradient id="onb-v-ffly" cx=".5" cy=".5" r=".5"><stop offset="0" stop-color="oklch(0.96 0.16 115)"/><stop offset=".35" stop-color="oklch(0.90 0.17 115)" stop-opacity=".6"/><stop offset="1" stop-color="oklch(0.90 0.17 115)" stop-opacity="0"/></radialGradient>
      <linearGradient id="onb-v-hb" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="oklch(0.26 0.05 205)"/><stop offset="1" stop-color="oklch(0.22 0.045 212)"/></linearGradient>
      <linearGradient id="onb-v-hf" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="oklch(0.31 0.06 180)"/><stop offset="1" stop-color="oklch(0.21 0.045 215)"/></linearGradient>
      <linearGradient id="onb-v-fade" gradientUnits="userSpaceOnUse" x1="-50" y1="0" x2="950" y2="0"><stop offset="0" stop-color="#fff" stop-opacity="0"/><stop offset=".12" stop-color="#fff" stop-opacity="1"/></linearGradient>
      <radialGradient id="onb-v-red" cx=".5" cy=".5" r=".5"><stop offset="0" stop-color="oklch(0.72 0.26 28)" stop-opacity=".95"/><stop offset="1" stop-color="oklch(0.60 0.25 25)" stop-opacity="0"/></radialGradient>
      <radialGradient id="onb-v-fire" cx=".5" cy=".5" r=".5"><stop offset="0" stop-color="oklch(0.80 0.18 58)" stop-opacity=".8"/><stop offset="1" stop-color="oklch(0.66 0.20 35)" stop-opacity="0"/></radialGradient>
      <mask id="onb-v-mask" maskUnits="userSpaceOnUse" x="-50" y="0" width="2050" height="700"><rect x="-50" y="0" width="2050" height="700" fill="url(#onb-v-fade)"/></mask>
    </defs>`

const BACK_PINES = () => [[40, 512, 46], [78, 506, 60], [250, 486, 48], [440, 470, 52], [600, 458, 42], [700, 452, 38], [880, 456, 50]].map(([x, b, h]) => pine(x, b, h)).join('')

export const GROUND_SVG = () => `<g mask="url(#onb-v-mask)">
      <path d="M-50 505 C 150 470, 320 482, 470 462 S 760 442, 950 456 C 1050 463, 1300 470, 2000 470 V700 H-50Z" fill="url(#onb-v-hb)"/>
      ${BACK_PINES()}
      <path d="M-50 562 C 160 546, 330 560, 520 548 S 780 540, 950 548 C 1050 553, 1300 556, 2000 556 V700 H-50Z" fill="url(#onb-v-hf)"/>
      <path d="M-50 692 C 120 672, 180 642, 300 638 S 480 624, 600 608 M722 602 C 780 596, 840 588, 950 572 C 1005 564, 1150 548, 2000 548" fill="none" stroke="oklch(0.40 0.04 65 / .5)" stroke-width="22" stroke-linecap="round"/>
      <path data-hit="river" d="M702 440 C 692 505, 642 545, 662 600 S 642 660, 622 700 L692 700 C 702 660, 722 620, 702 590 S 740 505, 728 440Z" fill="oklch(0.33 0.08 245)"/>
      <path class="water" d="M712 470 C 700 520, 660 550, 676 600 S 660 660, 652 700" fill="none" stroke="oklch(0.72 0.08 235 / .45)" stroke-width="1.5" stroke-dasharray="10 30"/>
      <path class="water" style="animation-delay:-1.4s" d="M720 450 C 712 520, 676 560, 690 600 S 672 660, 664 700" fill="none" stroke="oklch(0.72 0.08 235 / .35)" stroke-width="1.2" stroke-dasharray="6 34"/>
    </g>`

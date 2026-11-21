// What each step builds into the village, one plot per step. Each plot is a few
// pieces (.pc) that rise one after the other when it is built. Ported unchanged
// from docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.
import { LIT, WALL, WALL2, ROOF, DARK, WOOD, pc, glow, win, tree, burst, lantern, bulbs } from './art-ground.js'
import { P_BLUE, P_RED, P_GREEN, P_GOLD } from './art-people.js'
import { R_A, R_B } from './art-robots.js'
import { walker } from './art-figures.js'

/* the first house's secrets: the roof (the cat), the lit window (someone waves) */
const HOUSE_FX = `<g class="fx">
  <path data-hit="roof" d="M288 488 L355 430 L422 488Z" fill="transparent"/>
  <circle class="w2" cx="386" cy="503" r="22" fill="url(#onb-v-glow)"/><rect class="w2" x="376" y="494" width="20" height="18" rx="1.5" fill="oklch(0.97 0.08 95)"/>
  <g class="wv"><circle cx="384" cy="501" r="3" fill="oklch(0.80 0.06 60)"/><rect x="381" y="504" width="6" height="8" rx="2" fill="oklch(0.55 0.12 255)"/><g transform="translate(388 505)"><g class="lm a-wave" style="transform:rotate(180deg)"><rect x="-.9" y="0" width="1.8" height="6" rx=".9" fill="oklch(0.80 0.06 60)"/></g></g></g>
  <rect data-hit="window" x="372" y="490" width="28" height="26" fill="transparent"/>
  <g class="cat"><ellipse cx="392" cy="458" rx="7" ry="4.5" fill="oklch(0.22 0.02 290)"/><circle cx="399" cy="452" r="3.6" fill="oklch(0.22 0.02 290)"/><path d="M396.4 449.6 l.8 -3.8 l2.4 2.8z M400.4 449 l2 -3.2 l.6 3.6z" fill="oklch(0.22 0.02 290)"/><circle cx="400.8" cy="451.6" r=".8" fill="oklch(0.85 0.15 120)"/><path class="tail" d="M385.5 458 q-6 -2 -5 -10" stroke="oklch(0.22 0.02 290)" stroke-width="2" fill="none" stroke-linecap="round"/></g>
</g>`

export const PLOT_SVG = {
  /* step 1: the path into your village */
  path: () => pc(0, [[60, 682], [108, 669], [158, 655], [208, 644], [268, 639], [328, 636], [388, 630], [448, 623], [508, 616], [566, 609]].map(([x, y]) => `<ellipse cx="${x}" cy="${y}" rx="9" ry="3.5" fill="oklch(0.56 0.03 70)"/>`).join(''))
    + pc(1, `<path d="M120 664V626" stroke="${WOOD}" stroke-width="4"/><path d="M104 628h30l6 5-6 5h-30z M136 642h-28l-6 5 6 5h28z" fill="oklch(0.50 0.06 60)" stroke="${DARK}"/>`)
    + pc(2, lantern(250, 642, 40)) + burst(150, 640),
  /* step 2: the workshop */
  workshop: () => pc(0, `<rect x="142" y="508" width="76" height="46" fill="${WALL2}"/><rect x="142" y="508" width="76" height="46" fill="none" stroke="${DARK}"/><rect x="150" y="524" width="22" height="30" fill="${DARK}"/>`)
    + pc(1, `<path d="M200 500v-26h10v26" fill="oklch(0.30 0.03 40)"/><path d="M134 512 L180 486 L226 512Z" fill="oklch(0.34 0.06 260)"/>`)
    + pc(2, `${win(184, 522, 22, 16)}${glow(195, 530, 30, .7)}<g transform="translate(180 498)"><circle class="spin" r="7" fill="none" stroke="oklch(0.83 0.15 72)" stroke-width="3" stroke-dasharray="3 2.5"/></g>`)
    + `<g class="fx"><circle class="smoke" cx="205" cy="470" r="6" fill="oklch(0.6 0.02 290)"/><circle class="smoke" style="animation-delay:-2.5s" cx="205" cy="470" r="6" fill="oklch(0.6 0.02 290)"/></g>` + burst(180, 520),
  /* step 3: your first house */
  house: () => pc(0, `<rect x="300" y="482" width="110" height="94" fill="${WALL}"/><rect x="300" y="482" width="110" height="94" fill="none" stroke="${DARK}"/><path d="M300 530h110" stroke="${WALL2}" stroke-width="3"/>`)
    + pc(1, `<rect x="383" y="436" width="13" height="34" fill="oklch(0.32 0.04 40)"/><path d="M288 488 L355 430 L422 488Z" fill="${ROOF}" stroke="${DARK}"/><path d="M288 488 L355 430 L422 488" fill="none" stroke="oklch(0.55 0.10 35)" stroke-width="3"/>`)
    + pc(2, `${win(314, 494, 20, 18)}${win(376, 494, 20, 18)}${win(314, 540, 20, 20)}<rect x="362" y="542" width="24" height="34" rx="2" fill="${DARK}"/><circle cx="380" cy="560" r="1.6" fill="${LIT}"/>${glow(355, 520, 70, .55)}`)
    + pc(3, walker('human', 205, 638, 240, -10, 30, 0, P_BLUE)) + HOUSE_FX + burst(355, 520),
  /* step 4: the garden */
  garden: () => pc(0, tree(262, 584, 20) + `<circle class="fx" data-hit="tree" cx="262" cy="556" r="26" fill="transparent"/>`)
    + pc(1, Array.from({ length: 15 }, (_, k) => { const x = 286 + k * 10; return x > 350 && x < 392 ? '' : `<rect x="${x}" y="590" width="5" height="16" rx="1.5" fill="oklch(0.70 0.03 80)"/>` }).join('') + `<path d="M286 596h64M392 596h40" stroke="oklch(0.60 0.03 80)" stroke-width="2.5"/>`)
    + pc(2, `<g data-hit="flowers"><rect class="fx" x="290" y="604" width="150" height="20" fill="transparent"/>` + [[298, 616, 'oklch(0.78 0.14 10)'], [312, 620, 'oklch(0.86 0.14 88)'], [330, 614, 'oklch(0.76 0.12 320)'], [404, 617, 'oklch(0.86 0.14 88)'], [418, 621, 'oklch(0.78 0.14 10)'], [432, 615, 'oklch(0.76 0.12 320)']].map(([x, y, c]) => `<path d="M${x} ${y + 9}v-9" stroke="oklch(0.55 0.10 150)" stroke-width="1.6"/><g class="fl"><circle cx="${x}" cy="${y}" r="4" fill="${c}"/><circle cx="${x}" cy="${y}" r="1.4" fill="oklch(0.9 0.1 90)"/></g>`).join('') + '</g>')
    + pc(3, `<path d="M448 614v-18" stroke="${WOOD}" stroke-width="3"/><rect x="440" y="586" width="16" height="11" rx="5" fill="oklch(0.55 0.12 255)"/><path d="M454 586v-6h5" stroke="oklch(0.78 0.14 25)" stroke-width="2" fill="none"/>`) + burst(355, 600),
  /* step 5: the windmill, the engine */
  windmill: () => pc(0, `<path d="M500 552 L508 432 H532 L540 552Z" fill="oklch(0.58 0.02 70)" stroke="${DARK}"/><rect x="513" y="526" width="14" height="26" rx="6" fill="${DARK}"/><path d="M503 500h34M506 466h28" stroke="oklch(0.50 0.02 70)" stroke-width="2"/>`)
    + pc(1, `<path d="M503 434 L520 412 L537 434Z" fill="${ROOF}" stroke="${DARK}"/>${win(514, 474, 12, 14)}${glow(520, 482, 34, .6)}`)
    + pc(2, `<g class="spin" style="animation-duration:8s"><g class="sails" data-hit="windmill"><circle class="fx" cx="520" cy="420" r="70" fill="none"/>${[0, 90, 180, 270].map(a => `<g transform="rotate(${a} 520 420)"><path d="M520 420 V354" stroke="oklch(0.45 0.04 50)" stroke-width="3"/><rect x="521" y="356" width="13" height="56" fill="oklch(0.85 0.03 80 / .85)" stroke="oklch(0.45 0.04 50)"/><path d="M521 370h13M521 384h13M521 398h13" stroke="oklch(0.45 0.04 50)" stroke-width="1"/></g>`).join('')}<circle cx="520" cy="420" r="5" fill="oklch(0.40 0.04 50)"/></g></g>`) + burst(520, 440),
  /* step 6: the robots */
  robots: () => pc(0, `<path d="M193 694 a22 22 0 0 1 44 0z" fill="oklch(0.45 0.04 260)" stroke="${DARK}"/><rect x="208" y="680" width="14" height="14" rx="3" fill="${DARK}"/><circle cx="215" cy="677" r="3" class="blink" fill="oklch(0.86 0.12 165)"/>${glow(215, 684, 24, .5)}<circle class="fx" data-hit="dome" cx="215" cy="682" r="22" fill="transparent"/>`)
    + pc(1, walker('robot', 250, 692, 220, -4, 24, -6, R_A))
    + pc(2, walker('robot', 432, 614, 130, -6, 15, -2, R_B)) + burst(300, 670),
  /* step 7: the bridge to a second plot */
  bridge: () => pc(0, `<path d="M596 607 Q660 584 724 601" fill="none" stroke="${WOOD}" stroke-width="9"/><path d="M596 594 Q660 571 724 588" fill="none" stroke="oklch(0.48 0.05 55)" stroke-width="2.5"/>${[610, 630, 650, 670, 690, 710].map(x => `<path d="M${x} ${598 - Math.sin((x - 596) / 128 * Math.PI) * 11}v12" stroke="oklch(0.48 0.05 55)" stroke-width="2"/>`).join('')}`)
    + pc(1, `<rect x="752" y="520" width="78" height="66" fill="${WALL2}" stroke="${DARK}"/><path d="M744 524 L791 484 L838 524Z" fill="oklch(0.36 0.07 280)" stroke="${DARK}"/>${win(764, 534, 22, 18, 'oklch(0.80 0.10 285)')}${win(798, 534, 20, 18)}<rect x="800" y="558" width="18" height="28" rx="2" fill="${DARK}"/>${glow(788, 548, 52, .5)}`)
    + pc(2, tree(860, 590, 16, 'oklch(0.36 0.08 170)'))
    + pc(3, walker('human', 728, 614, 120, -6, 18, -4, P_GREEN)) + burst(700, 560),
  /* step 8: the gate and its lanterns */
  gate: () => pc(0, `<path d="M584 616V578M614 614V576" stroke="oklch(0.50 0.03 290)" stroke-width="5"/><path d="M582 580 Q599 556 616 578" fill="none" stroke="oklch(0.50 0.03 290)" stroke-width="4"/>`)
    + pc(1, `<path d="M599 566v6" stroke="oklch(0.3 0.03 290)"/><rect x="595" y="571" width="8" height="10" rx="2" fill="${LIT}"/>${glow(599, 576, 22, .9)}`)
    + pc(2, lantern(470, 628, 34) + lantern(738, 606, 32)) + burst(600, 590),
  /* step 9: the campfire and the lights */
  campfire: () => pc(0, `<ellipse cx="515" cy="672" rx="22" ry="6" fill="oklch(0.38 0.02 60)"/><path d="M498 674 L532 664 M498 664 L532 674" stroke="oklch(0.36 0.06 45)" stroke-width="5" stroke-linecap="round"/><path d="M474 682h26M532 682h26" stroke="${WOOD}" stroke-width="7" stroke-linecap="round"/>`)
    + pc(1, `${glow(515, 650, 80, 1)}<circle class="fx fireglow" cx="515" cy="645" r="70" fill="url(#onb-v-glow)"/><g class="fire" data-hit="fire"><circle class="fx" cx="515" cy="646" r="28" fill="transparent"/><path class="flick" d="M515 670 c-16 -10 -10 -30 0 -44 c3 14 16 18 12 34 c-2 7 -7 10 -12 10z" fill="oklch(0.72 0.18 45)"/><path class="flick b" d="M515 670 c-9 -6 -6 -18 0 -27 c2 9 9 11 7 20 c-1 4 -4 7 -7 7z" fill="oklch(0.90 0.15 85)"/>
      <g class="fx">${[[-4, 6], [3, -8], [6, 10], [-2, -4], [1, 4]].map(([o, sx], k) => `<circle class="spark" style="--sx:${sx}px;animation-delay:${(-k * .5).toFixed(1)}s" cx="${515 + o}" cy="640" r="1.6" fill="oklch(0.92 0.14 80)"/>`).join('')}</g></g>`)
    + pc(2, bulbs(215, 512, 255, 530, 296, 490, 6) + bulbs(420, 490, 465, 512, 506, 470, 7) + bulbs(536, 470, 640, 540, 748, 522, 12)) + burst(515, 650),
  /* step 10: the rest of the village */
  village: () => pc(0, `<rect x="748" y="436" width="46" height="36" fill="${WALL2}" stroke="${DARK}"/><path d="M742 440 L771 414 L800 440Z" fill="${ROOF}" stroke="${DARK}"/>${win(758, 448, 12, 11)}<rect x="778" y="452" width="10" height="20" fill="${DARK}"/>`)
    + pc(1, `<rect x="828" y="444" width="44" height="34" fill="${WALL}" stroke="${DARK}"/><path d="M822 448 L850 424 L878 448Z" fill="oklch(0.36 0.07 280)" stroke="${DARK}"/>${win(836, 455, 11, 10)}${win(854, 455, 11, 10)}${glow(810, 455, 60, .45)}`)
    + pc(2, tree(665, 478, 13, 'oklch(0.36 0.08 150)') + tree(460, 470, 12, 'oklch(0.34 0.07 165)') + tree(95, 560, 15))
    + pc(3, walker('human', 120, 678, 150, -8, 20, -7, P_RED) + walker('human', 732, 640, 140, -8, 21, -12, P_GOLD) + walker('robot', 330, 662, 110, -2, 14, -9, R_A))
    + `<g class="fx">${[[0, 0], [24, -12], [-20, 14]].map(([dx, dy]) => `<path class="bird" d="M${140 + dx} ${210 + dy} q4 -4 8 0 q4 -4 8 0" fill="none" stroke="oklch(0.25 0.05 300)" stroke-width="1.8"/>`).join('')}</g>` + burst(800, 450),
}

/* the windows that light up (and brighten with the windmill boost) */
export const LIT_WINDOW_FILLS = [LIT, 'oklch(0.80 0.10 285)']

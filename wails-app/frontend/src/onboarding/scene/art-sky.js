// The night sky, moon, aurora, sunrise and far mountains behind the village.
// Ported unchanged from docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html
// (ids prefixed with onb-, structural ids turned into classes).

function stars() {
  let h = ''
  let seed = 7
  const rnd = () => (seed = (seed * 9301 + 49297) % 233280) / 233280
  for (let i = 0; i < 140; i++) {
    const x = rnd() * 1600, y = rnd() * 640, r = rnd() < .9 ? rnd() * 1.3 + .4 : rnd() * 1.4 + 1.6
    const tw = rnd() < .18
    h += `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${r.toFixed(2)}" fill="oklch(0.95 0.02 290)" opacity="${(.35 + rnd() * .6).toFixed(2)}"${tw ? ` class="twinkle" style="animation-delay:${(-rnd() * 5).toFixed(2)}s"` : ''}/>`
  }
  return h
}

export const SKY_SVG = () => `<svg class="onb-world" viewBox="0 0 1600 1000" preserveAspectRatio="xMidYMax slice" aria-hidden="true">
  <defs>
    <linearGradient id="onb-sky" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="oklch(0.11 0.04 280)"/>
      <stop offset=".6" stop-color="oklch(0.17 0.07 295)"/>
      <stop offset="1" stop-color="oklch(0.25 0.09 325)"/>
    </linearGradient>
    <linearGradient id="onb-dawnG" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="oklch(0.22 0.07 285)" stop-opacity="0"/>
      <stop offset=".55" stop-color="oklch(0.45 0.13 20)" stop-opacity=".5"/>
      <stop offset="1" stop-color="oklch(0.78 0.14 65)" stop-opacity=".9"/>
    </linearGradient>
    <radialGradient id="onb-warmSkyG" cx=".72" cy="1" r=".7">
      <stop offset="0" stop-color="oklch(0.70 0.15 55)" stop-opacity=".55"/>
      <stop offset="1" stop-color="oklch(0.60 0.15 40)" stop-opacity="0"/>
    </radialGradient>
    <radialGradient id="onb-sunR" cx=".5" cy=".5" r=".5">
      <stop offset="0" stop-color="oklch(0.97 0.08 90)"/>
      <stop offset=".25" stop-color="oklch(0.88 0.14 75)" stop-opacity=".9"/>
      <stop offset="1" stop-color="oklch(0.75 0.16 50)" stop-opacity="0"/>
    </radialGradient>
    <radialGradient id="onb-moonG" cx=".5" cy=".5" r=".5">
      <stop offset="0" stop-color="oklch(0.95 0.02 280)" stop-opacity=".3"/>
      <stop offset="1" stop-color="oklch(0.95 0.02 280)" stop-opacity="0"/>
    </radialGradient>
    <linearGradient id="onb-aur" x1="0" y1="0" x2="1" y2="0">
      <stop offset="0" stop-color="oklch(0.70 0.16 330)" stop-opacity="0"/>
      <stop offset=".5" stop-color="oklch(0.78 0.15 60)" stop-opacity=".2"/>
      <stop offset="1" stop-color="oklch(0.70 0.16 330)" stop-opacity="0"/>
    </linearGradient>
    <linearGradient id="onb-starG" x1="0" y1="0" x2="1" y2="0"><stop offset="0" stop-color="oklch(0.98 0.03 90)"/><stop offset="1" stop-color="oklch(0.98 0.03 90)" stop-opacity="0"/></linearGradient>
    <linearGradient id="onb-mt1" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="oklch(0.27 0.06 300)"/><stop offset="1" stop-color="oklch(0.20 0.05 290)"/></linearGradient>
    <linearGradient id="onb-mt2" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="oklch(0.22 0.05 270)"/><stop offset="1" stop-color="oklch(0.17 0.04 265)"/></linearGradient>
  </defs>
  <rect data-hit="sky" width="1600" height="1000" fill="url(#onb-sky)"/>
  <rect class="onb-dawn" data-hit="sky" width="1600" height="1000" fill="url(#onb-dawnG)"/>
  <rect class="onb-warmsky" data-hit="sky" width="1600" height="1000" fill="url(#onb-warmSkyG)" opacity="0"/>
  <g class="onb-stars" data-hit="sky">${stars()}</g>
  <circle data-hit="sky" cx="1420" cy="140" r="130" fill="url(#onb-moonG)"/>
  <circle data-hit="sky" cx="1420" cy="140" r="24" fill="oklch(0.94 0.03 290)" opacity=".85"/>
  <circle data-hit="sky" cx="1430" cy="134" r="22" fill="oklch(0.14 0.05 288)" opacity=".9"/>
  <path class="drift" d="M-100 320 C 300 220, 600 410, 900 290 S 1500 210, 1750 310 L1750 370 C 1400 290, 1200 420, 900 360 S 300 290, -100 390Z" fill="url(#onb-aur)" data-hit="sky"/>
  <g class="onb-wfx"></g>
  <g class="onb-sun" data-hit="sky"><circle cx="1120" cy="700" r="300" fill="url(#onb-sunR)"/><circle cx="1120" cy="700" r="46" fill="oklch(0.96 0.09 88)"/></g>
  <path d="M0 720 L120 640 L230 700 L360 600 L470 690 L600 620 L720 700 L860 590 L990 680 L1120 610 L1260 690 L1380 620 L1500 680 L1600 640 L1600 1000 L0 1000Z" fill="url(#onb-mt1)"/>
  <path d="M0 800 C 160 760, 300 790, 480 770 S 820 740, 1000 770 S 1380 750, 1600 770 L1600 1000 L0 1000Z" fill="url(#onb-mt2)"/>
  <path d="M0 900 C 300 880, 700 895, 1000 885 S 1400 880, 1600 890 L1600 1000 L0 1000Z" fill="oklch(0.21 0.045 215)"/>
</svg>`

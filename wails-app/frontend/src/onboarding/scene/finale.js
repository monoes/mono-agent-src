// The last step only: the windmill boost (a click makes the house lights
// brighter for 3 s, another click restarts the 3 s, nothing accumulates), the
// parked drone (a click lifts it for about 6 s), and the secret combination
// (windmill 7+ times, then the campfire, then the drone, then the windmill
// again while the drone is still up) that sets off the rampage. Not a
// discovery, never counted or hinted.
// Ported from docs/mastermind/specs/onboarding-mockups/style-c-cinematic.html.

export function createFinale(ctx) {
  let boostT = 0
  const drone = { up: false, t: 0, a: null, landT: 0 }
  const seq = { wm: 0, stage: 0 }
  const droneEl = () => ctx.q('.onb-drone')

  function boost() { ctx.cancel(boostT); ctx.q('.onb-vsvg').classList.add('boost'); boostT = ctx.later(3000, boostOff) }
  function boostOff() { ctx.cancel(boostT); boostT = 0; ctx.q('.onb-vsvg').classList.remove('boost') }

  function droneTo(y, ms, ease) {
    const lift = droneEl().querySelector('.dlift')
    let from = ''
    try { from = getComputedStyle(lift).transform } catch { /* no styles */ }
    if (drone.a) drone.a.cancel()
    drone.a = ctx.anim(lift, [{ transform: from && from !== 'none' ? from : 'translate(0px,0px)' }, { transform: `translate(0px,${y}px)` }], { duration: ms, easing: ease, fill: 'forwards' })
    if (drone.a) lift.removeAttribute('transform'); else lift.setAttribute('transform', `translate(0 ${y})`)
  }
  function droneFly() {
    ctx.cancel(drone.t)
    if (!drone.up) { drone.up = true; droneEl().classList.add('dup'); droneTo(-64, 800, 'cubic-bezier(.3,.8,.4,1)') }
    drone.t = ctx.later(6000, droneLand)
  }
  function droneLand() {
    if (ctx.ramp.active() || !drone.up) return
    drone.up = false; if (seq.stage === 2) seqReset()
    droneTo(0, 900, 'ease-in-out')
    ctx.cancel(drone.landT)
    drone.landT = ctx.later(900, () => { if (!drone.up && !ctx.ramp.active()) droneEl().classList.remove('dup') })
  }
  /* the rampage keeps the drone up until it is over */
  function holdDrone() { ctx.cancel(drone.t); drone.t = 0 }
  function droneReset() {
    ctx.cancel(drone.t); ctx.cancel(drone.landT); drone.up = false
    if (drone.a) drone.a.cancel(); drone.a = null
    const d = droneEl(); if (!d) return
    d.classList.remove('dup')
    d.querySelectorAll('.dmove, .dlift, .dtilt').forEach(g => g.removeAttribute('transform'))
  }

  function seqReset() { seq.wm = 0; seq.stage = 0 }
  /* true when this click set the rampage off */
  function secret(k) {
    if (!ctx.isLast()) return false
    if (k === 'windmill') {
      if (seq.stage === 2 && drone.up) { seqReset(); ctx.ramp.start(); return true }
      if (seq.stage) seqReset()
      seq.wm++
    } else if (k === 'fire' && !seq.stage && seq.wm >= 7) seq.stage = 1
    else if (k === 'drone' && seq.stage === 1) seq.stage = 2
    else seqReset()
    return false
  }

  /* leaving the last step: lights back to normal, the combination starts over */
  function leave() { if (boostT) boostOff(); seqReset() }

  return { boost, boostOff, droneFly, holdDrone, droneReset, secret, seqReset, leave, get droneUp() { return drone.up } }
}

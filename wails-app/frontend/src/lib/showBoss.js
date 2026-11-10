// Show the open run's boss on the Agents page: navigate there, wait (bounded) for the boss section to mount, scroll it into view
// and focus it. The page is not in the sidebar, so this is how a run's boss is reached from the chat, the notice and the Control room.
// The wait is cancellable: a newer call cancels the one before it, and so does the person doing anything else (a key press or a
// click) while it waits, so the focus never moves after they went elsewhere.
export const BOSS_SECTION = '[data-testid="open-run-boss"]'

let current = 0 // the token of the wait that may still act

let stopCurrent = null // ends the wait that may still act: clears its timers and removes its listeners

export function cancelShowBoss() { current++; stopCurrent?.() }

export function showBossInAgents(navigate, { timeoutMs = 4000, intervalMs = 100, find = () => document.querySelector(BOSS_SECTION) } = {}) {
  if (typeof navigate !== 'function') return Promise.resolve(false)
  cancelShowBoss()
  const mine = ++current
  navigate('ai')
  return new Promise(resolve => {
    const t0 = Date.now()
    let finished = false
    let listening = false
    let armTimer = null
    let tickTimer = null
    const onUser = () => cancelShowBoss()
    const unlisten = () => {
      if (!listening) return
      listening = false
      if (typeof document === 'undefined') return // the page is gone: there is nothing to unhook
      document.removeEventListener('keydown', onUser, true)
      document.removeEventListener('pointerdown', onUser, true)
    }
    const done = ok => {
      finished = true
      clearTimeout(armTimer)
      clearTimeout(tickTimer)
      unlisten()
      if (stopCurrent === stop) stopCurrent = null
      resolve(ok)
    }
    const stop = () => done(false)
    stopCurrent = stop
    // the click that started this wait is already over; only a later one counts as going elsewhere. Nothing is registered for a
    // wait that has already ended (the section was there at once), and every listener added is removed when it ends.
    armTimer = setTimeout(() => {
      if (finished || mine !== current) return
      if (typeof document === 'undefined') return
      listening = true
      document.addEventListener('keydown', onUser, true)
      document.addEventListener('pointerdown', onUser, true)
    }, 0)
    const tick = () => {
      if (mine !== current || typeof document === 'undefined') return done(false) // cancelled, or the page was torn down
      const el = find()
      if (el) {
        el.scrollIntoView?.({ block: 'center' })
        el.focus?.()
        done(true)
      } else if (Date.now() - t0 >= timeoutMs) done(false)
      else tickTimer = setTimeout(tick, intervalMs)
    }
    tick()
  })
}

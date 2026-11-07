// Show the open run's boss on the Agents page: navigate there, wait (bounded) for the boss section to mount, scroll it into view
// and focus it. The page is not in the sidebar, so this is how a run's boss is reached from the chat, the notice and the Control room.
export const BOSS_SECTION = '[data-testid="open-run-boss"]'

export function showBossInAgents(navigate, { timeoutMs = 4000, intervalMs = 100, find = () => document.querySelector(BOSS_SECTION) } = {}) {
  if (typeof navigate !== 'function') return Promise.resolve(false)
  navigate('ai')
  return new Promise(resolve => {
    const t0 = Date.now()
    const tick = () => {
      const el = find()
      if (el) {
        el.scrollIntoView?.({ block: 'center' })
        el.focus?.()
        resolve(true)
      } else if (Date.now() - t0 >= timeoutMs) resolve(false)
      else setTimeout(tick, intervalMs)
    }
    tick()
  })
}

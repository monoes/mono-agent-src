// Counts the agent test runs the Your agents step started that have not
// closed yet, across remounts of the step. Go runs one validation at a time:
// a new start kills the running one, whose "agents:validateClosed" (an error
// such as "validation cancelled…") then arrives after the new start. A test
// started while earlier runs are still open skips that many closed events
// before it takes one as its own end. The count lives here, with its own
// listener, because the step may be unmounted when an earlier run closes.
import { subscribeEvent } from '../services/api.js'

let open = 0
let off = null

function listen() {
  if (!off) off = subscribeEvent('agents:validateClosed', () => { if (open > 0) open -= 1 })
}

/** Call right before StartAgentValidation; returns how many earlier runs may close first. */
export function beginAgentTestRun() {
  listen()
  const before = open
  open += 1
  return before
}

/** The start was refused: no run, so no closed event will come for it. */
export function refusedAgentTestRun() {
  if (open > 0) open -= 1
}

/** Back to no open runs, as in a freshly opened window (tests). */
export function resetAgentTestRuns() {
  if (off) { off(); off = null }
  open = 0
}

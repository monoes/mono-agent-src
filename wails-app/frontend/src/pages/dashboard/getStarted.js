// The dashboard's first-run checklist and the bridge row's state — display
// only, from data the dashboard already reads: the account gate, `workflow
// list`, `workflow executions`, `summary` and `extension status --json`.

import { execStatus } from '../../lib/execStatus.js'

// bridgeState is what the "Browser bridge" row says: connected, waiting (for
// a browser), unpaired (this client's pairing token is turned away) or off
// (no bridge). `summary` only probes the port, so `extension status`, when it
// answered, decides — a bridge that rejects this client is not "connected".
export function bridgeState(summaryBridge, ext) {
  if (ext) {
    if (!ext.running) return 'off'
    if (ext.clientPairing === 'mismatch' || ext.bridge?.status === 'unpaired') return 'unpaired'
    if (ext.bridge?.status === 'connected' || ext.bridge?.status === 'waiting') return ext.bridge.status
  }
  return summaryBridge ? summaryBridge.status || 'off' : 'off'
}

// getStartedSteps: sign in, run a first workflow (from a template) that
// succeeds (okRuns: successful runs), connect
// the browser extension, and — optional — keep the daemon running for
// schedules. Each step: { id, done, optional }.
export function getStartedSteps({ signedIn, okRuns, bridge, daemonRunning }) {
  return [
    { id: 'signIn', done: !!signedIn },
    { id: 'firstRun', done: okRuns > 0 },
    { id: 'extension', done: bridge === 'connected' },
    { id: 'daemon', done: !!daemonRunning, optional: true },
  ]
}

// okRunCount: how many of the recent executions succeeded — a failed or
// partly failed run does not tick "Run your first workflow".
export function okRunCount(executions) {
  return (executions || []).filter(e => execStatus(e.status).key === 'success').length
}

export const getStartedComplete = (steps) => steps.every(s => s.done || s.optional)

// Dismissing the checklist is remembered per window, like the scope toggle;
// a blocked storage just shows it again.
export const DISMISS_KEY = 'monoagent.dashboard.getStartedDismissed'

export function loadDismissed(storage = globalThis.localStorage) {
  try { return storage?.getItem(DISMISS_KEY) === '1' } catch { return false }
}

export function clearDismissed(storage = globalThis.localStorage) {
  try { storage?.removeItem(DISMISS_KEY) } catch { /* nothing remembered to clear */ }
}

export function saveDismissed(storage = globalThis.localStorage) {
  try { storage?.setItem(DISMISS_KEY, '1') } catch { /* hidden until the window closes */ }
}

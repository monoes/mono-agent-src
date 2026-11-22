// The bridge row's state (from `summary` and `extension status --json`) and
// whether the dashboard's setup card was hidden (FinishSetupCard; the key is
// the one the earlier Get started checklist used, so hiding it still holds).

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

// Hiding the setup card is remembered per window, like the scope toggle;
// a blocked storage just shows it again.
export const DISMISS_KEY = 'monoagent.dashboard.getStartedDismissed'

export function loadDismissed(storage = globalThis.localStorage) {
  try { return storage?.getItem(DISMISS_KEY) === '1' } catch { return false }
}

export function saveDismissed(storage = globalThis.localStorage) {
  try { storage?.setItem(DISMISS_KEY, '1') } catch { /* hidden until the window closes */ }
}

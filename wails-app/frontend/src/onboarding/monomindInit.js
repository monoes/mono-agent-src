// A tiny store for the onboarding's "Turn on monomind" run. It lives outside
// the step component so a run keeps going, and its result still lands, after
// the person moved on to another step: InitializeMonomindProfile returns at
// once and reports through "monomind:initProgress" events ({kind: "line" |
// "error" | "done", message}), see wails-app/app_monomind_init.go.
//
// Events count only while this store has a run going: while it is idle, the
// runs the Documents, Orgs and Agents pages start (same init, same event) are
// ignored. While it runs, Go refuses any other start with an "already being
// set up" error on the same event; that refusal is not this run's failure
// (this store's own refused start is read from the binding's return value).
// On "done" the store calls the reload it was given, mounted step or not, so
// the shell's state reads monomind as on.
import * as GoApp from '../wailsjs/go/main/App'
import { subscribeEvent } from '../services/api.js'
import { callStep } from './callStep.js'

const MAX_LINES = 200
// wails-app/app_monomind_init.go InitializeMonomindProfile's refusal of a
// second run, emitted as an "error" event and returned.
const BUSY_REFUSAL = "this profile's folder is already being set up"
const IDLE = Object.freeze({ status: 'idle', profileId: '', lines: [], message: '', loginRequired: false })

let snap = IDLE
let off = null
let reloadFn = null
const listeners = new Set()

function set(patch) {
  snap = { ...snap, ...patch }
  for (const fn of listeners) fn()
}

function stopListening() {
  if (off) { off(); off = null }
}

function onEvent(ev) {
  if (snap.status !== 'running' || !ev) return
  if (ev.kind === 'line') {
    set({ lines: [...snap.lines, String(ev.message ?? '')].slice(-MAX_LINES) })
  } else if (ev.kind === 'error') {
    if (String(ev.message || '').includes(BUSY_REFUSAL)) return
    stopListening()
    set({ status: 'error', message: String(ev.message || ''), loginRequired: false })
  } else if (ev.kind === 'done') {
    stopListening()
    set({ status: 'done' })
    // useOnboarding's reload resolves to a callStep result and never rejects.
    if (reloadFn) reloadFn()
  }
}

/** The current run: { status: idle|running|error|done, profileId, lines, message, loginRequired }. */
export function getMonomindInit() {
  return snap
}

/** Calls fn on every change; returns the unsubscribe. */
export function subscribeMonomindInit(fn) {
  listeners.add(fn)
  return () => { listeners.delete(fn) }
}

/**
 * Starts the init for the active profile (profileId only labels the run, so
 * a later profile does not read an earlier one's result). reload is called
 * once the run reports done. A second start while one runs does nothing.
 */
export async function startMonomindInit(reload, profileId = '') {
  if (snap.status === 'running') return
  reloadFn = reload || null
  // Listen before starting: a failure to start is both emitted and returned.
  stopListening()
  off = subscribeEvent('monomind:initProgress', onEvent)
  set({ status: 'running', profileId, lines: [], message: '', loginRequired: false })
  const r = await callStep(() => GoApp.InitializeMonomindProfile())
  if (!r.ok && snap.status === 'running') {
    stopListening()
    set({ status: 'error', message: r.message, loginRequired: r.loginRequired })
  }
}

/** Back to idle, as in a freshly opened window (tests). */
export function resetMonomindInit() {
  stopListening()
  reloadFn = null
  snap = IDLE
  for (const fn of listeners) fn()
}

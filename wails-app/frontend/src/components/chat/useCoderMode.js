import { useCallback, useEffect, useState } from 'react'
import { api } from '../../services/api.js'

// Coder mode (#203) state for the chat panel: the CLI's `coder status`
// (enabled / ready / defaults / per-runtime readiness) and the recent
// workspaces. Everything comes from monoagentcli through the api wrappers;
// nothing is decided here beyond what to show.

// coderRuntimes is status.runtimes ([{id, installed, fullAccess, ready,
// toolActivity, resume, effort, maxTurns, reportsCost, initTarget}]), or —
// from a monoagentcli that predates per-runtime coder mode — the one
// runtime it names (claude), ready when the status is.
export function coderRuntimes(status) {
  if (!status) return []
  if (Array.isArray(status.runtimes)) return status.runtimes
  const id = status.runtime || 'claude'
  const ready = status.ready !== false
  return [{ id, installed: true, fullAccess: true, ready, toolActivity: 'full', resume: true, effort: true, maxTurns: true, reportsCost: true, initTarget: 'claude' }]
}

// coderReadyIds lists the runtimes a coder chat can run on now.
export function coderReadyIds(status) {
  return coderRuntimes(status).filter(r => r.ready).map(r => r.id)
}

// coderReady: coder chats can start (some runtime is ready). A status from
// before per-runtime readiness keeps its own ready flag.
export function coderReady(status) {
  if (!status) return false
  if (Array.isArray(status.runtimes)) return status.runtimes.some(r => r.ready)
  return status.ready !== false
}

// runtimeReadiness is one runtime's readiness in words.
export function runtimeReadiness(rt) {
  if (rt.ready) return 'ready'
  if (rt.installed === false) return 'not installed'
  if (!rt.fullAccess) return 'no full access in this monomind'
  return 'not ready'
}

// fidelityNote says what a runtime's tool cards leave out, or '' when it
// reports every call with its result.
export function fidelityNote(rt) {
  if (!rt || !rt.toolActivity || rt.toolActivity === 'full') return ''
  if (rt.toolActivity === 'none') return `${rt.id} shows no tool calls, only its replies`
  return `${rt.id} shows commands, not every result`
}

// folderName is the last path segment ("…/20260927-brisk-otter").
export function folderName(path) {
  if (!path) return ''
  const parts = String(path).replace(/[\\/]+$/, '').split(/[\\/]/)
  return parts[parts.length - 1] || path
}

export function isCoderConversation(c) {
  return c?.mode === 'coder'
}

// MAX_PROBE_ERROR bounds the probe failure shown in a pane.
const MAX_PROBE_ERROR = 200

// monomindUnusable is the probe failure when monomind could not be run at
// all (not found, a shim that does not resolve, a crash), else ''. That is
// not an outdated monomind, so nothing is said about capabilities or updates.
export function monomindUnusable(status) {
  const err = typeof status?.probeError === 'string' ? status.probeError.replace(/\s+/g, ' ').trim() : ''
  const points = Array.from(err) // by code point: a slice could split a surrogate pair
  return points.length > MAX_PROBE_ERROR ? `${points.slice(0, MAX_PROBE_ERROR).join('')}…` : err
}

// monomindNeedsFix: the status calls for the "Fix monomind" action, either
// because monomind could not be used or because it lacks capabilities.
export function monomindNeedsFix(status) {
  return !!monomindUnusable(status) || (status?.missingCapabilities || []).length > 0
}

// missingText explains a not-ready status: that monomind could not be used
// (with the reason), which monomind capabilities coder mode still needs
// (with the version found), or — when monomind has them all — that no coding
// runtime is ready.
export function missingText(status) {
  const unusable = monomindUnusable(status)
  if (unusable) return `needs a working monomind (monomind couldn't be used: ${unusable})`
  const missing = status?.missingCapabilities || []
  if (missing.length) {
    const found = status?.monomindVersion ? `found monomind ${status.monomindVersion}, ` : ''
    return `needs monomind update (${found}missing: ${missing.join(', ')})`
  }
  if (Array.isArray(status?.runtimes)) return 'needs a coding runtime (claude, codex, opencode, …) installed'
  return 'needs monomind update'
}

// useCoderStatus loads `coder status` once `active` (the panel is open and
// can offer coder mode), and again on refresh(). A failed call (e.g. an
// older monoagentcli without `coder`, or no Wails bridge at all) reads as
// "not enabled" — started inside a promise so a synchronous throw lands
// in the same catch.
export function useCoderStatus(active) {
  const [status, setStatus] = useState(null)
  const refresh = useCallback(() => {
    return Promise.resolve().then(() => api.coderStatus()).then(setStatus).catch(() => setStatus(null))
  }, [])
  useEffect(() => {
    if (active) refresh()
  }, [active, refresh])
  return { status, refresh }
}

// useRecentWorkspaces loads `coder workspace list` while `active`.
export function useRecentWorkspaces(active) {
  const [recent, setRecent] = useState([])
  useEffect(() => {
    if (!active) return
    let current = true
    Promise.resolve().then(() => api.coderWorkspaceList())
      .then(list => { if (current) setRecent((Array.isArray(list) ? list : []).filter(w => w.exists !== false)) })
      .catch(() => { if (current) setRecent([]) })
    return () => { current = false }
  }, [active])
  return recent
}

// useActiveProfile loads the active profile ({name, root_dir, …}) while
// `active`; null until known or when it can't be read. A new coder chat
// works in that profile's folder unless the user picks another.
export function useActiveProfile(active) {
  const [profile, setProfile] = useState(null)
  useEffect(() => {
    if (!active) return
    let current = true
    Promise.resolve().then(() => api.getActiveProfile())
      .then(p => { if (current) setProfile(p && p.name ? p : null) })
      .catch(() => { if (current) setProfile(null) })
    return () => { current = false }
  }, [active])
  return profile
}

// useCoderRuntimeChoice narrows the runtime picker for a coder chat being
// set up: `options` are the scanned runtimes coder mode can run on now, and
// a current runtime outside them is swapped for the first that is — so the
// model list shown (fetched per runtime) is always the chosen runtime's
// own. `info` is the chosen runtime's coder status entry (fidelity, cost
// reporting, …). active is false outside coder mode and once the
// conversation exists (its runtime is fixed then).
export function useCoderRuntimeChoice({ active, status, runtimes, selectedRuntime, setSelectedRuntime }) {
  const readyIds = coderReadyIds(status)
  const options = runtimes.filter(r => readyIds.includes(r.id))
  const optionKey = options.map(r => r.id).join(',')
  useEffect(() => {
    if (!active || options.length === 0 || options.some(r => r.id === selectedRuntime)) return
    setSelectedRuntime(options[0].id)
    // options is rebuilt every render; optionKey is its identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, optionKey, selectedRuntime, setSelectedRuntime])
  const info = coderRuntimes(status).find(r => r.id === selectedRuntime) || null
  return { options, readyIds, info }
}

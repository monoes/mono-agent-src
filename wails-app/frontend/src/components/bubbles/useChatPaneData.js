import { useCallback, useEffect, useState } from 'react'
import { api } from '../../services/api.js'
import { cachedAgentScan } from '../../lib/agentRuntimes.js'
import { accessOfConversation } from '../../lib/chatAccess.js'
import { chatAccessApi } from '../../lib/chatAccessApi.js'
import { aitaskBudgetApi } from '../settings/aitaskBudgetApi.js'

// What the pane's settings popover reads about one open conversation, loaded
// when `active` (the popover is open). Each part fails on its own: a missing
// part reads as null and the popover says so.

// useBrowserLevel: the level for an agent kind from `browser permissions`
// ({ level, label, isDefault }), or null.
export function useBrowserLevel(kind, active) {
  const [level, setLevel] = useState(undefined)
  useEffect(() => {
    if (!active) return undefined
    let live = true
    Promise.resolve().then(() => api.browserPermissions())
      .then(res => {
        if (!live) return
        const row = (res?.kinds || []).find(k => k.kind === kind)
        const label = (res?.catalog || []).find(c => c.id === row?.level)?.label || row?.level
        setLevel(row ? { level: row.level, label, isDefault: !!row.isDefault } : null)
      })
      .catch(() => { if (live) setLevel(null) })
    return () => { live = false }
  }, [kind, active])
  return level
}

// useCostVsCap: { spentUsd, capUsd, scope } for a conversation bound to a task
// (that task's cap), else the global cap from the budget list; null when none
// is set or it cannot be read.
export function useCostVsCap(taskId, active, version = 0) {
  const [cap, setCap] = useState(undefined)
  useEffect(() => {
    if (!active) return undefined
    let live = true
    const read = taskId
      ? aitaskBudgetApi.forTask(taskId).then(b => (b?.capUsd > 0 ? { spentUsd: b.reported ? b.spentUsd : null, capUsd: b.capUsd, scope: 'task' } : null))
      : aitaskBudgetApi.list().then(b => {
        const global = (b?.caps || []).find(c => c.scope === 'global')
        return global && global.capUsd > 0 ? { spentUsd: global.usedUsd ?? b.spentUsd ?? null, capUsd: global.capUsd, scope: 'global' } : null
      })
    read.then(v => { if (live) setCap(v) }).catch(() => { if (live) setCap(null) })
    return () => { live = false }
  }, [taskId, active, version])
  return cap
}

// useVerifiedAccess resolves a conversation's authoritative record (access,
// org mode, redacted title) from the backend. A cached level is never trusted:
// until the lookup answers the pane treats the level as unknown, with its
// controls and the composer off. Resolves state 'loading' | 'ready' | 'failed';
// refresh() reads the record again (after a turn, for the title) and retry()
// after a failure. A bubble whose level a history row or a create just gave
// (verified) needs no lookup.
export function useVerifiedAccess(bubble, store) {
  const { key, conversationId, verified } = bubble
  const [failed, setFailed] = useState(false)
  const [tick, setTick] = useState(0)
  const load = useCallback(() => Promise.resolve().then(() => chatAccessApi.record(conversationId)).then(rec => {
    const access = accessOfConversation(rec)
    if (!access) throw new Error('no access level in the record')
    const patch = { access, orgMode: rec.orgMode === 'dynamic' ? 'dynamic' : '', verified: true }
    // The title is the backend's redacted one; a chat in a folder keeps the folder's name unless the person named it.
    if ((!rec.cwd || rec.renamed) && typeof rec.title === 'string') patch.title = rec.title
    store.patchBubble(key, patch)
    setFailed(false)
  }).catch(() => setFailed(true)), [key, conversationId, store])
  useEffect(() => {
    if (!conversationId || verified) return undefined
    load()
    return undefined
    // load is stable per conversation; tick re-runs it after a failure.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conversationId, verified, tick])
  const state = !conversationId || verified ? 'ready' : failed ? 'failed' : 'loading'
  const retry = useCallback(() => { setFailed(false); setTick(n => n + 1) }, [])
  return { state, retry, refresh: load }
}

// useStreamsIncrementally: whether a scoped chat's runtime streams its reply as
// it goes (the scan entry's streams_incrementally), which decides what the turn
// status says while it waits. True until the scan says otherwise.
export function useStreamsIncrementally(runtime, active) {
  const [streams, setStreams] = useState(true)
  useEffect(() => {
    if (!active || !runtime) return undefined
    let live = true
    Promise.resolve().then(() => cachedAgentScan())
      .then(res => { if (live) setStreams((res?.agents || []).find(a => a.id === runtime)?.streams_incrementally !== false) })
      .catch(() => {})
    return () => { live = false }
  }, [runtime, active])
  return streams
}

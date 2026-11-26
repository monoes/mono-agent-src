import { useEffect, useReducer, useRef } from 'react'
import { api, onChatEvent } from '../../services/api.js'
import { chatReducer, initialChatState } from './chatReducer.js'
import i18nOr from '../../lib/i18nOr.js'

const PAGE_SIZE = 200
// One automatic retry of a failed backlog fetch, after this pause.
const HYDRATE_RETRY_MS = 1000

// useChatStream observes one turn's chat:event stream: hydrate the durable
// backlog, subscribe live, merge the two without gaps or duplicates, and
// hand the result through chatReducer. Purely observational — starting or
// stopping a turn is the caller's job (api.startChatTurn/stopChatTurn); this
// hook only ever reads.
//
// Subscribe-before-fetch, buffered hydration, gap catch-up and a
// generation-counter stale-request guard all live here so chatReducer stays
// a plain, synchronous, fully unit-testable function.
export function useChatStream({ conversationId, turnId }) {
  const [state, dispatch] = useReducer(chatReducer, initialChatState())

  useEffect(() => {
    if (!conversationId || !turnId) {
      dispatch({ type: 'reset' })
      return
    }
    dispatch({ type: 'scope', scope: { conversationId, turnId } })

    let cancelled = false
    let hydrating = true
    let localLastSeq = 0
    let pendingLive = []

    function belongsHere(ev) {
      return ev.conversationId === conversationId && ev.turnId === turnId
    }

    function dispatchEvent(ev) {
      // fillGap has no upper bound on what it fetches, so the same event
      // can legitimately reach this function twice — once via a gap-fill
      // page that happens to already include it, once via the live
      // delivery that triggered that gap-fill (or any other redundant
      // re-delivery). The reducer's own 'event' case already no-ops a
      // seq it has seen before; alreadyApplied lets the side effect below
      // share that exact same notion of "did this call actually advance
      // anything," instead of firing once per dispatchEvent call.
      const alreadyApplied = typeof ev.seq === 'number' && ev.seq <= localLastSeq
      if (typeof ev.seq === 'number' && ev.seq > localLastSeq) localLastSeq = ev.seq
      dispatch({ type: 'event', event: ev })
      // historySaved:false does NOT mean this hook's own transcript is
      // wrong — everything applied above is the authoritative live record.
      // It means the store's durable write fell behind, so a *future*
      // reopen of this conversation (loadConversation -> getChatTurns) may
      // not show everything just shown here. Nothing to reconcile against
      // (a refetch now could only return LESS than what's already applied)
      // — the only correct response is telling the user, via the same
      // notice banner real backend 'notice' events already render.
      // A turn that FAILED (or was refused) before producing history is not
      // that case: it has no history to lose, and the bubble shows its
      // reason, so the banner would only mislead.
      if (!alreadyApplied && ev.type === 'turn.finished' && ev.payload?.historySaved === false && ev.payload?.status !== 'failed') {
        dispatch({
          type: 'localNotice',
          notice: {
            code: 'history_not_saved',
            message: i18nOr('ui7Chat.history_not_saved'),
            severity: 'warning',
          },
        })
      }
    }

    // Fetches [afterSeq+1 .. beforeSeq-1] before applying the event that
    // jumped ahead — "fetch missing gaps; never infer sequence continuity
    // from the highest event seen."
    async function fillGap(beforeSeq) {
      for (;;) {
        const startSeq = localLastSeq
        const res = await api.getChatEvents(conversationId, turnId, startSeq, PAGE_SIZE)
        if (cancelled) return
        const items = Array.isArray(res?.items) ? res.items : []
        for (const e of items) dispatchEvent(e)
        if (items.length === 0) return
        if (localLastSeq >= beforeSeq - 1 || !res?.hasMore) return
        if (localLastSeq === startSeq) return // no forward progress — avoid looping forever
      }
    }

    // A failed fetch must never swallow what the user is watching: the live
    // event is still applied, and one notice says earlier events may be
    // missing (shown once per stream, however many fetches fail).
    let degradedNoticed = false
    function noticeDegraded() {
      if (degradedNoticed) return
      degradedNoticed = true
      dispatch({
        type: 'localNotice',
        notice: {
          code: 'stream_backlog_failed',
          message: i18nOr('ui7Chat.backlog_failed'),
          severity: 'warning',
        },
      })
    }

    async function dispatchLive(ev) {
      if (typeof ev.seq === 'number' && ev.seq > localLastSeq + 1) {
        try {
          await fillGap(ev.seq)
        } catch {
          if (cancelled) return
          noticeDegraded()
        }
        if (cancelled) return
      }
      dispatchEvent(ev)
    }

    const unsubscribe = onChatEvent((raw) => {
      if (cancelled || !belongsHere(raw)) return
      if (hydrating) {
        pendingLive.push(raw)
        return
      }
      dispatchLive(raw).catch(() => {})
    })

    let retryTimer = null
    async function hydrate(canRetry = true) {
      try {
        await loadBacklog()
      } catch {
        if (cancelled) return
        if (canRetry) {
          retryTimer = setTimeout(() => { hydrate(false) }, HYDRATE_RETRY_MS)
          return
        }
        noticeDegraded()
      }
      if (cancelled) return
      hydrating = false
      const buffered = pendingLive.slice().sort((a, b) => a.seq - b.seq)
      pendingLive = []
      for (const e of buffered) {
        if (cancelled) return
        // eslint-disable-next-line no-await-in-loop
        await dispatchLive(e)
      }
    }

    async function loadBacklog() {
      let afterSeq = 0
      for (;;) {
        const res = await api.getChatEvents(conversationId, turnId, afterSeq, PAGE_SIZE)
        if (cancelled) return
        const items = Array.isArray(res?.items) ? res.items : []
        for (const e of items) dispatchEvent(e)
        if (items.length === 0) break
        afterSeq = localLastSeq
        if (!res?.hasMore) break
      }
    }

    hydrate()

    return () => {
      cancelled = true
      clearTimeout(retryTimer)
      unsubscribe()
    }
  }, [conversationId, turnId])

  return state
}

// ── Historical turn loading ──────────────────────────────────────────────────
//
// reduceTurnEvents/loadTurnState are the historical sibling of hydrate()
// above: loading a PAST turn's already-fetched events (a one-shot backlog
// fetch, no live merge, no subscription) rather than observing a live one.
// Used by the chat bubble's transcript loader to rebuild each past turn's
// state before feeding it to the same ChatTimeline/TurnStatus components a
// live turn (via useChatStream above) uses.

// Replays one turn's already-fetched events through chatReducer to
// reconstruct its final state — used for history (a past turn's events,
// fetched once) rather than live streaming (this file's own useChatStream
// hook owns that). No scope is set: the caller already fetched precisely
// one turn's events via getChatEvents(conversationId, turnId, ...), so
// every event necessarily belongs here. The result is fed straight into
// ChatTimeline/TurnStatus — the same components a live turn uses — so a
// reopened past turn renders identically to how it looked while it was
// still running.
export function reduceTurnEvents(events) {
  return events.reduce((state, event) => chatReducer(state, { type: 'event', event }), initialChatState())
}

// Fetches one turn's full event backlog (paginating past a single page,
// same as hydrate() above) and returns its reduced state, or null for a
// turn that produced nothing at all and never reached a terminal status
// (defensively skipped rather than shown as a blank turn).
//
// That skip must NOT apply to a turn that is active because it is
// genuinely still running in a DIFFERENT live instance (GetChatTurns'
// ownedByThisInstance:false — see
// docs/mastermind/plans/2026-09-12-interactive-agent-chat-followups.md,
// "OwnerInstanceID is written and read back but never compared to
// anything"): turn.started alone adds no part, so a foreign turn that has
// only just been admitted elsewhere (routine while its subprocess launches
// or its model connects) has exactly the zero-parts shape this skip was
// meant for a DIFFERENT case (this instance's own turn, just admitted, no
// events yet) — silently dropping it here would hide it entirely, right as
// this window's own next send() in that same conversation gets refused
// (ErrTurnOwnedByOtherInstance), with nothing on screen explaining why.
// Same principle the live-turn finalize path already applies (see its own
// comment in the old assistant panel: "Pushed even when parts is empty: TurnStatus
// alone still truthfully reports a silent failure/stop rather than hiding
// it") — an empty ChatTimeline plus the honest static TurnStatus label is
// the correct, non-hiding rendering here too.
export async function loadTurnState(conversationId, turn) {
  let afterSeq = 0
  let events = []
  for (;;) {
    const page = await api.getChatEvents(conversationId, turn.id, afterSeq, 200)
    const items = Array.isArray(page?.items) ? page.items : []
    events = events.concat(items)
    if (items.length === 0 || !page?.hasMore) break
    afterSeq = items[items.length - 1].seq
  }
  const state = reduceTurnEvents(events)
  if (state.parts.length === 0 && turn.status === 'active' && turn.ownedByThisInstance !== false) return null
  return state
}

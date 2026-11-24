import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useMountedTimers } from '../lib/useMountedTimers.js'
import {
  X, RefreshCw, Building2, Circle, Network, Maximize2, Plus,
  Coins, GitBranch, ScrollText, ListTree, Play, Loader2, UserCheck, Gavel, Boxes,
  ChevronLeft, ChevronRight, Inbox, MessageCircle, FileText,
} from 'lucide-react'
import { api, onOrgEvent, onOrgEventsClosed, onOrgDesignUpdated, onOrgRunStatus, notify } from '../services/api.js'
import OrgDesigner from './orgdesigner/OrgDesigner.jsx'
import { KVBlock } from './KVBlock.jsx'
import { OrgTemplatesButton, PublishOrgButton } from './orgs/OrgLibraryActions.jsx'
import { acquireOrgEvents, orgEventHolders, restartOrgEvents } from '../lib/orgEventStreams.js'
import { emitOpenOrgBubble } from '../lib/appEvents.js'
import MonomindInitPrompt from './MonomindInitPrompt.jsx'
import AutonomyBar from './orgs/AutonomyBar.jsx'
import NeedsYouPanel from './orgs/NeedsYouPanel.jsx'
import DecisionsFeed from './orgs/DecisionsFeed.jsx'
import DocumentsPanel from './orgdesigner/DocumentsPanel.jsx'
import ScheduleAuditPanel from './orgdesigner/ScheduleAuditPanel.jsx'
import GroupView from './orgs/GroupView.jsx'
import QueuedMessagesPanel from './orgs/QueuedMessagesPanel.jsx'
import useNeedsYouCounts from './orgs/useNeedsYouCounts.js'
import useQueuedCount from './orgs/useQueuedCount.js'
import { Badge, Chip } from './orgs/ui.jsx'
import FullAccessSummary from './orgs/FullAccessSummary.jsx'
import OrgSignatureBanner, { isSignatureRefusal, requestSignatureRefresh } from './orgs/OrgSignatureBanner.jsx'
import OrgToolActivity from './orgs/OrgToolActivity.jsx'
import SectionBudgetPanel from './orgs/SectionBudgetPanel.jsx'
import { isToolActivity } from './orgs/orgToolActivity.js'

// Fold button shown atop the expanded org-list panel — mirrors
// OrgDesigner.jsx's panelFoldBtnStyle for visual consistency between the
// two foldable-rail patterns in this app.
const panelFoldBtnStyle = {
  display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 4, flexShrink: 0,
  fontFamily: 'var(--font-mono)', fontSize: 9.5, padding: '5px 6px',
  background: 'transparent', border: 'none', borderBottom: '1px solid var(--border)',
  color: 'var(--text-muted)', cursor: 'pointer',
}

// Narrow vertical strip shown in place of the folded org list — click to
// expand. Mirrors OrgDesigner.jsx's panelStripStyle.
function panelStripStyle(borderSide) {
  const base = {
    width: 26, flexShrink: 0,
    display: 'flex', alignItems: 'flex-start', justifyContent: 'center', paddingTop: 10,
    background: 'transparent', border: 'none',
    color: 'var(--text-muted)', cursor: 'pointer',
  }
  return borderSide === 'right'
    ? { ...base, borderRight: '1px solid var(--border)' }
    : { ...base, borderLeft: '1px solid var(--border)' }
}

// `needs` replaces the old Approvals tab (its auto-approve toggle is gone —
// C-42; autonomy levels resolve decisions in the daemon instead). `decisions`
// is the autonomy decision log; monomind's own decision trace moved to
// `trace`. `group` only shows for holding orgs. `queued` lists messages
// waiting for the org's next start (C-35).
const TABS = [
  { id: 'design',     labelKey: 'orgs.tabs.design',     icon: Network },
  { id: 'overview',   labelKey: 'orgs.tabs.overview',   icon: Circle },
  { id: 'group',      labelKey: 'orgs.tabs.group',      icon: Boxes, holdingOnly: true },
  { id: 'needs',      labelKey: 'orgs.tabs.needs',      icon: UserCheck },
  { id: 'queued',     labelKey: 'orgs.tabs.queued',     icon: Inbox },
  { id: 'decisions',  labelKey: 'orgs.tabs.decisions',  icon: Gavel },
  { id: 'documents',  labelKey: 'orgs.tabs.documents',  icon: FileText },
  { id: 'logs',       labelKey: 'orgs.tabs.logs',       icon: ScrollText },
  { id: 'costs',      labelKey: 'orgs.tabs.costs',      icon: Coins },
  { id: 'flow',       labelKey: 'orgs.tabs.flow',       icon: GitBranch },
  { id: 'trace',      labelKey: 'orgs.tabs.trace',      icon: ListTree },
]

function statusColor(status) {
  if (status === 'running') return 'var(--green-neon)'
  if (status === 'crashed' || status === 'error') return 'var(--red)'
  if (status === 'paused') return '#eab308'
  return 'var(--text-muted)'
}

function StatusDot({ status }) {
  const c = statusColor(status)
  return <span style={{ width: 6, height: 6, borderRadius: '50%', background: c, boxShadow: c !== 'var(--text-muted)' ? `0 0 5px ${c}` : 'none', flexShrink: 0 }} />
}

function itemsOf(payload) {
  if (!payload) return []
  if (Array.isArray(payload.items)) return payload.items
  if (Array.isArray(payload)) return payload
  return []
}

// The org's actual deliverable (the post/document/answer it produced) isn't
// a distinct field anywhere — org_complete's own `summary` is often just a
// vague meta-note ("post was created and approved") rather than the verbatim
// result, and orgs rarely emit `asset` events. What IS reliable: the
// coordinator (report.outcome.by) almost always narrates the finished result
// in its own words as its last chat message right after calling
// org_complete. Pull that out so Overview can show it prominently instead of
// making people dig through the raw event log for it.
function extractFinalMessage(events, outcome) {
  if (!outcome?.by) return null
  const list = itemsOf(events)
  const chats = list.filter(e => e.type === 'chat' && e.from === outcome.by)
  if (!chats.length) return null
  return chats[chats.length - 1].msg || null
}

// ── Detail JSON dump — the org shapes are intentionally opaque JSON, so a
// key/value table renders whatever monomind actually returned instead of
// guessing field names and silently dropping ones we didn't anticipate.
// (KVBlock itself now lives in ./KVBlock.jsx — see that file for why.) ──

function Card({ children }) {
  return (
    <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '10px 14px' }}>
      {children}
    </div>
  )
}

// Overview's promoted "what actually happened" summary — a colored outcome
// banner plus, when we could find one, the org's own words describing its
// result (see extractFinalMessage above). Renders nothing if there's no
// outcome yet (run still going, or never called org_complete).
function OutcomeBanner({ outcome, finalMessage }) {
  if (!outcome) return null
  const color = outcome.status === 'achieved' ? 'var(--green-neon)'
    : outcome.status === 'partial' ? '#eab308'
    : outcome.status === 'failed' ? 'var(--red)'
    : 'var(--text-muted)'
  const label = outcome.status === 'achieved' ? 'Achieved'
    : outcome.status === 'partial' ? 'Partial'
    : outcome.status === 'failed' ? 'Failed'
    : outcome.status || 'Unknown'
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{
        display: 'flex', alignItems: 'center', gap: 6, padding: '6px 10px',
        background: 'var(--surface)', border: `1px solid ${color}`, borderRadius: 'var(--radius)',
      }}>
        <span style={{ width: 7, height: 7, borderRadius: '50%', background: color, boxShadow: `0 0 5px ${color}`, flexShrink: 0 }} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color, fontWeight: 600, letterSpacing: 0.5, textTransform: 'uppercase' }}>{label}</span>
        {outcome.by && <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>by {outcome.by}</span>}
      </div>
      {finalMessage && (
        <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '12px 14px', fontSize: 12.5, color: 'var(--text)', lineHeight: 1.5, whiteSpace: 'pre-wrap' }}>
          {finalMessage}
        </div>
      )}
    </div>
  )
}

// OpenAsBubbleButton opens the org as a chat bubble (#229): its live stage
// and a chat with its boss, over whatever page is open.
function OpenAsBubbleButton({ orgName, compact = false }) {
  const { t } = useTranslation()
  return (
    <button type="button" className="btn btn-ghost btn-sm" data-testid="org-open-bubble"
      onClick={() => emitOpenOrgBubble(orgName)}
      title={t('orgBubble.openAsBubble')} aria-label={t('orgBubble.openAsBubbleNamed', { name: orgName })}
      style={compact ? { padding: '3px 5px', flexShrink: 0 } : { gap: 4, fontSize: 10 }}>
      <MessageCircle size={compact ? 11 : 12} />{!compact && ` ${t('orgBubble.openAsBubble')}`}
    </button>
  )
}

export default function OrgsPanel({ embedded = false, isOpen = true, onClose, pageActive = true, onNavigate, pendingSelectOrgName, onConsumePendingSelect }) {
  const timers = useMountedTimers()
  // Effective openness: an embedded panel is open when its isOpen prop says
  // so; a page-mode panel (rendered by the Orgs page) stays mounted under
  // App.jsx's keep-alive navigation, so for it "open" additionally requires
  // the Orgs page to be the active tab — otherwise its SSE stream would
  // keep running (and the event tail growing) forever after the user
  // navigated away (FV4-10).
  const effectiveOpen = embedded ? isOpen : pageActive
  const [orgs, setOrgs] = useState([])
  const [loadingOrgs, setLoadingOrgs] = useState(true)
  const [selected, setSelected] = useState(null) // org name
  const { t } = useTranslation()
  const [tab, setTab] = useState('overview')
  const [data, setData] = useState({}) // { [tab]: payload }
  const [tabLoading, setTabLoading] = useState(false)
  const [events, setEvents] = useState([])
  const [designerFullscreen, setDesignerFullscreen] = useState(false) // Design tab: collapse the org rail + tab strip for canvas room
  const [orgListOpen, setOrgListOpen] = useState(true) // manual fold, independent of designerFullscreen's auto-collapse
  const [creatingOrg, setCreatingOrg] = useState(false)
  const [newOrgName, setNewOrgName] = useState('')
  const [newOrgGoal, setNewOrgGoal] = useState('')
  const [createBusy, setCreateBusy] = useState(false)
  const [createError, setCreateError] = useState('')
  const [notInitialized, setNotInitialized] = useState(false)
  const [runningOrgs, setRunningOrgs] = useState(() => new Set())
  const [runPromptOpen, setRunPromptOpen] = useState(false)
  const [runTaskText, setRunTaskText] = useState('')
  // Overview's run picker: runsList is every run (current + historical).
  // selectedRun is null (no explicit choice yet — defaults to the most
  // recent PAST run once loaded), the literal string 'live' (only ever set
  // by pressing Play, or by the user manually picking it from the
  // dropdown — opening/switching to an org must never auto-select it just
  // because the org happens to still be running), or a specific run id.
  const [runsList, setRunsList] = useState([])
  const [selectedRun, setSelectedRun] = useState(null)
  const [streamGeneration, setStreamGeneration] = useState(0) // bumped to force the live event tail to re-subscribe against the current run
  const [runDetail, setRunDetail] = useState(null) // { run, loading, report, logs } for a selected historical run

  const loadOrgs = useCallback(async (silent = false) => {
    if (!silent) setLoadingOrgs(true)
    try {
      // Checked first: ListOrgDesigns silently succeeds with an empty list
      // on a never-initialized profile (EnsureLayout creates an empty
      // .monomind/ on every org call), so it can't distinguish "no orgs
      // yet" from "monomind was never set up here" on its own.
      const initialized = await api.isMonomindInitialized()
      setNotInitialized(!initialized)
      if (!initialized) { setOrgs([]); return 0 }
      // listOrgDesigns reads config files directly (wails-app/app_orgs_design.go)
      // and so includes every org, including ones designed but never run —
      // listOrgs (below) proxies `monoagentcli org list`'s RUNTIME view,
      // which only knows about orgs that have actually been started at
      // least once. The rail needs the former or a freshly-designed org is
      // invisible until its first run.
      const res = await api.listOrgDesigns()
      const items = itemsOf(res)
      setOrgs(items)
      return items.length
    } finally {
      if (!silent) setLoadingOrgs(false)
    }
  }, [])

  // First activation loads with the spinner; every later re-activation
  // (coming back to the Orgs page / reopening an embedded panel) silently
  // refreshes the org list so it doesn't drift while hidden.
  const firstLoadRef = useRef(true)
  useEffect(() => {
    if (!effectiveOpen) return
    const isFirst = firstLoadRef.current
    if (!isFirst) {
      loadOrgs(true)
      return
    }
    firstLoadRef.current = false
    // The very first load of the app's session can race Go's own startup
    // (the selected profile is only known once App.startup() has resolved
    // it — Wails doesn't guarantee the frontend waits for that) — landing on
    // Orgs before it resolves queries no profile, so the panel shows an
    // error or the "not set up" prompt until the user navigates away and
    // back. Poll App.IsReady() (cheap, in-memory,
    // set as the very last line of startup()) instead of guessing with a
    // fixed delay — a cold start with the social build's node-registry
    // scan can take longer than a one-shot timer accounts for.
    let cancelled = false
    let started = false
    ;(async () => {
      for (let i = 0; i < 30 && !cancelled; i++) { // ~6s ceiling — startup finishing this slow would be a real problem elsewhere too
        if (await api.isReady()) break
        await new Promise(r => setTimeout(r, 200))
      }
      if (cancelled) return
      // silent:false (the real bug this whole effect was chasing) — the
      // prior version called loadOrgs(isFirst), which for the very first
      // activation passes isFirst=true straight through as the silent
      // argument. silent=true skips BOTH setLoadingOrgs(true) (harmless,
      // already true from useState's initial value) AND, critically,
      // setLoadingOrgs(false) in loadOrgs' own finally block — so the
      // spinner that's already showing from mount never clears. The panel
      // then sits on its loading state forever until some LATER activation
      // (this effect's isFirst=false branch above) finally passes
      // silent=false for the first time and clears it — exactly "first
      // click shows nothing, navigate away and back and it loads".
      started = true
      loadOrgs(false)
    })()
    return () => {
      cancelled = true
      // React StrictMode (development only) double-invokes every effect on
      // initial mount: run it, run its cleanup, run it again. firstLoadRef
      // is a ref, so it survives that cleanup — meaning the FIRST
      // invocation's `firstLoadRef.current = false` above was still visible
      // to the SECOND invocation, which then wrongly took the `!isFirst`
      // silent branch and never called loadOrgs(false) at all. The result:
      // orgs loads correctly in the background, but the spinner (already
      // true from useState's initial value) never clears — permanently,
      // since firstLoadRef had already been consumed. Un-claiming
      // firstLoadRef here, but ONLY when this invocation's own polling
      // never actually reached loadOrgs(false) (`started` is still false),
      // fixes that: the surviving invocation correctly sees isFirst=true
      // again and runs the real sequence. A genuine completed first load
      // (started=true) leaves firstLoadRef consumed as before, so later
      // re-activations still correctly take the fast silent-refresh path.
      if (!started) firstLoadRef.current = true
    }
  }, [effectiveOpen, loadOrgs])

  // ── Tab data loading ──
  const loadTab = useCallback(async (orgName, tabId) => {
    if (!orgName) return
    // The Design tab owns its own load/save lifecycle (OrgDesigner.jsx
    // calls api.getOrgDesign itself, live-patched via onOrgDesignUpdated) —
    // it doesn't use this generic JSON-dump-per-tab path at all.
    // Needs you, Decisions, and Group likewise load and poll on their own.
    if (tabId === 'design' || tabId === 'needs' || tabId === 'decisions' || tabId === 'documents' || tabId === 'group') return
    setTabLoading(true)
    try {
      let payload = null
      switch (tabId) {
        case 'overview': {
          const [status, history, current] = await Promise.all([
            api.getOrgStatus(orgName),
            api.getOrgReport(orgName, true),
            api.getOrgReport(orgName, false),
          ])
          payload = status
          // "Live" must be gated on the org's ACTUAL status (`running`),
          // not merely on "this run is absent from history.jsonl" — a run
          // can be missing from history for reasons other than currently
          // running (e.g. it crashed instead of stopping cleanly), and
          // treating that as "live" mislabeled a dead org as working.
          const isRunning = status?.status === 'running'
          const liveRun = isRunning && current?.run && !current?.error ? current : null
          const historyItemsAll = itemsOf(history)
          const historyItems = liveRun ? historyItemsAll.filter(h => h.run !== liveRun.run) : historyItemsAll
          setRunsList([
            ...(liveRun ? [{ run: liveRun.run, live: true, summary: liveRun }] : []),
            ...historyItems.map(h => ({ run: h.run, live: false, summary: h })),
          ])
          // Default to the most recent PAST run, never to Live — opening
          // or switching to an org should show what it did, not silently
          // jump to "live" just because it happens to still be running.
          // "Live" is only ever entered by pressing Play (handleRunOrg
          // sets 'live' directly) or picking it from the dropdown by hand
          // — both of which leave selectedRun non-null, so this default
          // computation is skipped and the explicit choice is kept.
          setSelectedRun(prev => (prev === null && historyItems.length > 0) ? historyItems[0].run : prev)
          break
        }
        // Scoped to whichever run is selected in the (shared, tab-strip-level)
        // run picker — 'live' means "let the CLI default to the newest run"
        // (matches what Live is already tailing), same as Overview's own
        // runDetail effect does for report/logs. No run picked yet (org has
        // no runs) → skip the fetch entirely; itemsOf(null) already renders
        // each tab's existing empty state.
        case 'logs': {
          const run = selectedRun === 'live' ? '' : selectedRun
          payload = run || selectedRun === 'live' ? await api.getOrgLogs(orgName, run) : null
          break
        }
        case 'costs': {
          const run = selectedRun === 'live' ? '' : selectedRun
          payload = run || selectedRun === 'live' ? await api.getOrgCosts(orgName, run) : null
          break
        }
        case 'flow': {
          const run = selectedRun === 'live' ? '' : selectedRun
          payload = run || selectedRun === 'live' ? await api.getOrgFlow(orgName, run) : null
          break
        }
        case 'trace': {
          const run = selectedRun === 'live' ? '' : selectedRun
          payload = run || selectedRun === 'live' ? await api.getOrgDecisions(orgName, run) : null
          break
        }
        default: break
      }
      setData(prev => ({ ...prev, [tabId]: payload }))
    } finally {
      setTabLoading(false)
    }
  }, [selectedRun])

  useEffect(() => {
    if (selected) loadTab(selected, tab)
  }, [selected, tab, loadTab])

  // ── Overview's run picker: fetch a specific historical run's own report
  // + logs when picked. Live (selectedRun === 'live') needs nothing extra
  // — it's covered by the org status card + live event tail below. ──
  useEffect(() => {
    if (tab !== 'overview' || !selected || !selectedRun || selectedRun === 'live') { setRunDetail(null); return }
    let cancelled = false
    setRunDetail({ run: selectedRun, loading: true })
    Promise.all([
      api.getOrgReport(selected, false, selectedRun),
      api.getOrgLogs(selected, selectedRun),
    ]).then(([report, logs]) => {
      if (!cancelled) setRunDetail({ run: selectedRun, loading: false, report, logs })
    })
    return () => { cancelled = true }
  }, [tab, selected, selectedRun])

  // ── Live event tail ──
  // `streamGeneration` exists purely to force a restart: the underlying CLI
  // subscription (`org events --follow`, no explicit --run) resolves "which
  // run to tail" once, at subprocess start, and never re-resolves after —
  // so a subscription opened while an org was idle (or mid an older run)
  // keeps tailing that same stale/absent run forever, even once a brand new
  // run starts. Bumping this deliberately tears down and reopens the
  // subscription so it re-resolves to whatever run is current now.
  useEffect(() => {
    // Embedded panels render null when closed, but hooks still run — guard the
    // SSE stream + listeners on effectiveOpen so a closed panel (or a page-mode
    // panel whose page isn't active) stops streaming.
    if (!selected || !effectiveOpen) return
    setEvents([])
    const release = acquireOrgEvents(selected)
    // Another view (an org bubble) already holds this org's tail: restart
    // it, so it follows the current run and replays it into this list.
    if (orgEventHolders(selected) > 1) restartOrgEvents(selected)
    const offEvent = onOrgEvent((payload) => {
      if (payload?.orgName !== selected) return
      setEvents(prev => {
        const next = [...prev, payload.event]
        return next.length > 300 ? next.slice(-300) : next
      })
    })
    const offClosed = onOrgEventsClosed(() => {})
    return () => {
      offEvent()
      offClosed()
      release()
    }
  }, [selected, effectiveOpen, streamGeneration])

  // ── Needs you counts — one badge per org in the list, polled whichever
  // tab is open so waiting work is visible without clicking in. The open
  // Needs you tab reports fresher counts for its own org as it polls. ──
  const orgNames = orgs.map(o => o.name)
  const [needsYouCounts, setNeedsYouCounts] = useNeedsYouCounts(orgNames, effectiveOpen && !notInitialized)
  const handleNeedsYouCount = useCallback((count) => {
    if (!selected) return
    setNeedsYouCounts(prev => (prev[selected] === count ? prev : { ...prev, [selected]: count }))
  }, [selected, setNeedsYouCounts])
  // Messages waiting for the selected org's next start (C-35), counted on
  // its Queued tab. The open tab reports fresher counts as it reloads.
  const [queuedCount, setQueuedCount] = useQueuedCount(selected, effectiveOpen && !notInitialized)
  const selectedOrgMeta = orgs.find(o => o.name === selected) || null
  const isHolding = selectedOrgMeta?.kind === 'holding'

  // ── Run status tracking — independent of `selected` so the rail's
  // running-state indicator (if ever added) and this panel's Run button
  // both stay correct even if the user switches orgs mid-run. ──
  useEffect(() => {
    return onOrgRunStatus((payload) => {
      if (!payload?.orgName) return
      setRunningOrgs(prev => {
        const next = new Set(prev)
        if (payload.status === 'running') next.add(payload.orgName)
        else next.delete(payload.orgName)
        return next
      })
      // Only a genuine failure is worth an error toast — "stopped" just
      // means the process exited, which includes completely normal runs.
      // notify()/Toasts.jsx render every call as a red "Failed: …" (no
      // success/info variant exists), so routing a clean finish through it
      // misleadingly reads as a crash. The Overview view (Running… badge
      // clearing, run picker updating) already communicates completion.
      if (payload.status === 'error') notify('org run', payload.message || `${payload.orgName} exited with an error`)
      // monomind 2.21 refused an unsigned or changed org: bring up the
      // Review & sign banner.
      if (payload.status === 'error' && isSignatureRefusal(payload.message)) requestSignatureRefresh(payload.orgName)
      // A run actually starting/stopping is the moment the "Live" view's
      // data (status card, run dropdown) goes stale — refresh it right
      // then, not just on tab/org switch. Scoped to "currently looking at
      // Live for this exact org" so it never disturbs someone deliberately
      // inspecting a historical run or a different org.
      //
      // On "running" specifically: this event fires the instant the OS
      // process starts (RunOrg's cmd.Start() succeeding), but monomind's
      // own daemon takes a beat longer to actually write status:"running"
      // to runtime.json — a single reload right here can (and did) still
      // observe the pre-run state and never pick up the live run. Poll a
      // few times over a couple seconds instead of reloading once, so the
      // dropdown reliably catches the real transition once it lands.
      //
      // The live event tail has the exact same race, but one level worse:
      // it's not just reading stale data, its subscription is pinned to
      // whatever run existed when it was opened (see the effect's own
      // comment) and never re-resolves on its own. Bump streamGeneration
      // on each poll tick too, so the tail's subscription gets torn down
      // and reopened until one of those reopens lands after the new run
      // directory actually exists and picks it up.
      if (payload.orgName === selected && tab === 'overview' && selectedRun === 'live') {
        if (payload.status === 'running') {
          let attempts = 0
          const iv = timers.interval(() => {
            attempts++
            loadTab(selected, 'overview')
            setStreamGeneration(g => g + 1)
            if (attempts >= 6) timers.clear(iv)
          }, 600)
        } else {
          loadTab(selected, 'overview')
        }
      }
    })
  }, [selected, tab, selectedRun, loadTab, timers])

  const handleRunOrg = useCallback(async () => {
    if (!selected) return
    const task = runTaskText.trim()
    setRunPromptOpen(false)
    setRunTaskText('')
    setRunningOrgs(prev => new Set(prev).add(selected)) // optimistic — org:runStatus confirms shortly after
    // Land on Overview watching the just-started run live, per the ask —
    // scoped to this button press only, so a run starting elsewhere never
    // yanks the view out from under someone inspecting a different run/tab.
    // The actual data refresh happens off the org:runStatus "running" event
    // below (not here) — the process hasn't started yet at this point, so
    // reloading now would just re-show the previous/finished run's data.
    setTab('overview')
    setSelectedRun('live')
    // The "Live event tail" effect only clears `events` on org-switch or
    // panel open/close — pressing Run again on the org you're ALREADY
    // looking at never triggers that, so the box stayed full of whatever
    // the previous run left behind. Clear it here, with an immediate
    // synthetic entry so there's visible feedback before the org's real
    // events start arriving over the live stream (which itself lags a
    // beat behind the process actually starting, same as the status card).
    setEvents([{ type: 'status', msg: `Starting run for ${selected}…` }])
    const res = await api.runOrg(selected, task)
    if (res?.error) {
      notify('org run', res.error, res.code)
      setEvents(prev => [...prev, { type: 'status', msg: `Failed to start: ${res.error}` }])
      setRunningOrgs(prev => {
        const next = new Set(prev)
        next.delete(selected)
        return next
      })
    }
  }, [selected, runTaskText])

  const selectOrg = useCallback((name, { tab: tabOverride } = {}) => {
    setSelected(name)
    setTab(tabOverride || 'overview')
    setData({})
    setRunPromptOpen(false)
    setRunTaskText('')
    setRunsList([])
    setSelectedRun(null)
  }, [])

  // New orgs created externally (e.g. by the chat, which shells out to a
  // separate monomind subprocess) never trigger this component's own state
  // updates — they only show up via the filesystem-watcher-driven
  // org:designUpdated event. Auto-select any org this component doesn't
  // already know about into the Design tab, so its role cards animate in
  // live as the chat creates them (see OrgDesigner.jsx's applyLivePatch).
  // Scoped to genuinely-new orgs only — an edit to an org already in the
  // rail shouldn't yank the user's view away from what they're doing.
  //
  // Selecting it internally isn't enough if the user is on a different
  // page (most likely Chat, since that's what's creating it) — they'd
  // never see it happen. onNavigate (App.jsx's page switcher, wired in via
  // pages/Orgs.jsx) brings them here too, same as pressing the Orgs tab
  // themselves. Only wired for the real page (App.jsx passes it); an
  // embedded/modal usage of this panel has no onNavigate and stays put.
  const orgsRef = useRef(orgs)
  useEffect(() => { orgsRef.current = orgs }, [orgs])
  useEffect(() => {
    const off = onOrgDesignUpdated((payload) => {
      if (!payload?.orgName) return
      if (orgsRef.current.some(o => o.name === payload.orgName)) return
      loadOrgs(true).then(() => selectOrg(payload.orgName, { tab: 'design' }))
      onNavigate?.('orgs')
    })
    return off
  }, [loadOrgs, selectOrg, onNavigate])

  // Settings › TypeSafe Jev: switch to Settings, then bring its Jev section
  // (id "settings-jev") into view once the page has rendered.
  const openJevSettings = useCallback(() => {
    onNavigate?.('settings')
    let tries = 0
    const scroll = () => {
      const el = document.getElementById('settings-jev')
      if (el) el.scrollIntoView({ block: 'start' })
      else if (++tries < 20) timers.timeout(scroll, 50)
    }
    timers.timeout(scroll, 0)
  }, [onNavigate, timers])

  // Covers the case the listener above can't: this panel didn't exist yet
  // when the org was created (App.jsx's own top-level listener caught it
  // instead, since pages here only mount after being visited once — see
  // Orgs.jsx). Runs once per pending name, on mount or whenever a new one
  // arrives while already mounted; onConsumePendingSelect clears it so it
  // doesn't re-fire if this component happens to remount later.
  useEffect(() => {
    if (!pendingSelectOrgName) return
    // A bare name (a newly created org) opens its design; the dashboard
    // passes { name, tab } to land on the tab it linked to.
    const pending = typeof pendingSelectOrgName === 'string' ? { name: pendingSelectOrgName, tab: 'design' } : pendingSelectOrgName
    loadOrgs(true).then(() => {
      selectOrg(pending.name, { tab: pending.tab || 'overview' })
      // A run picked from the History drawer (selectOrg cleared the selection just above).
      if (pending.run) setSelectedRun(pending.run)
    })
    onConsumePendingSelect?.()
  }, [pendingSelectOrgName, loadOrgs, selectOrg, onConsumePendingSelect])


  // Refresh button: reload the org rail AND, if an org/tab is currently
  // open, re-fetch that tab's data too — "refresh" should mean the whole
  // visible page, not just the rail.
  const [refreshingAll, setRefreshingAll] = useState(false)
  const refreshAll = async () => {
    setRefreshingAll(true)
    try {
      await loadOrgs()
      if (selected) await loadTab(selected, tab)
    } finally {
      setRefreshingAll(false)
    }
  }

  const handleCreateOrg = async () => {
    const name = newOrgName.trim()
    if (!name) { setCreateError('name required'); return }
    setCreateBusy(true)
    setCreateError('')
    try {
      const res = await api.createOrgDesign({ name, goal: newOrgGoal.trim() })
      if (!res || res.error) {
        setCreateError(res?.error || 'failed to create org')
        return
      }
      setCreatingOrg(false)
      setNewOrgName('')
      setNewOrgGoal('')
      await loadOrgs(true)
      selectOrg(name, { tab: 'design' })
    } finally {
      setCreateBusy(false)
    }
  }

  const containerStyle = embedded
    ? { width: 420, flexShrink: 0, background: '#060b13', borderLeft: '1px solid rgba(0,180,216,0.1)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }
    : { flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }

  if (embedded && !isOpen) return null

  return (
    <div style={containerStyle}>
      {embedded && (
        <div style={{ padding: '10px 12px', borderBottom: '1px solid rgba(0,180,216,0.1)', display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0 }}>
          <Building2 size={13} style={{ color: 'var(--text-muted)' }} />
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 700, color: '#e2e8f0', flex: 1, letterSpacing: 1 }}>ORGS</span>
          <button onClick={refreshAll} disabled={refreshingAll} title="Refresh" style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 2, display: 'flex' }}>
            <RefreshCw size={12} style={{ animation: refreshingAll ? 'spin 0.7s linear infinite' : 'none' }} />
          </button>
          {onClose && (
            <button onClick={onClose} title="Close panel" style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 2, display: 'flex' }}>
              <X size={14} />
            </button>
          )}
        </div>
      )}

      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        {/* Org list — auto-collapsed while the Design canvas is in
            fullscreen, or while the profile isn't set up (the CTA below is
            the only thing shown then — one prompt, not a rail full of "no
            orgs" plus a second one in the detail pane) — and additionally
            manually foldable via orgListOpen for canvas room on demand,
            mirroring OrgDesigner's palette/inspector fold pattern. */}
        {!notInitialized && !(designerFullscreen && tab === 'design') && (
        orgListOpen ? (
        <div style={{ width: embedded ? 130 : 220, flexShrink: 0, borderRight: '1px solid var(--border)', overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
          <button onClick={() => setOrgListOpen(false)} title="Collapse org list" style={panelFoldBtnStyle}>
            <ChevronLeft size={11} /> Collapse
          </button>
          <div style={{ flex: 1, overflowY: 'auto', padding: 8, display: 'flex', flexDirection: 'column', gap: 8 }}>
          {!creatingOrg ? (
            <button
              onClick={() => setCreatingOrg(true)}
              style={{
                display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 4,
                fontFamily: 'var(--font-mono)', fontSize: 10.5, padding: '6px 8px', borderRadius: 'var(--radius)',
                background: 'var(--elevated)', border: '1px solid var(--border-active)', color: 'var(--text)', cursor: 'pointer',
              }}
            >
              <Plus size={12} /> New org
            </button>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, padding: 8, background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)' }}>
              <input
                autoFocus
                placeholder="org-name"
                value={newOrgName}
                onChange={e => setNewOrgName(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') handleCreateOrg(); if (e.key === 'Escape') setCreatingOrg(false) }}
                style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, padding: '5px 6px', background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 4, color: 'var(--text)' }}
              />
              <input
                placeholder="goal (optional)"
                value={newOrgGoal}
                onChange={e => setNewOrgGoal(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') handleCreateOrg(); if (e.key === 'Escape') setCreatingOrg(false) }}
                style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, padding: '5px 6px', background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 4, color: 'var(--text)' }}
              />
              {createError && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: '#f87171' }}>{createError}</div>}
              <div style={{ display: 'flex', gap: 6 }}>
                <button onClick={handleCreateOrg} disabled={createBusy} style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 6px', borderRadius: 4, background: 'var(--elevated)', border: '1px solid var(--border-active)', color: 'var(--text)', cursor: createBusy ? 'default' : 'pointer' }}>
                  {createBusy ? 'Creating…' : 'Create'}
                </button>
                <button onClick={() => { setCreatingOrg(false); setCreateError('') }} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 4, background: 'transparent', border: '1px solid var(--border)', color: 'var(--text-muted)', cursor: 'pointer' }}>
                  Cancel
                </button>
              </div>
            </div>
          )}
          <OrgTemplatesButton onInstalled={res => { loadOrgs(true).then(() => { if (res?.local_id) selectOrg(res.local_id, { tab: 'design' }) }) }} />
          {loadingOrgs ? (
            <div style={{ display: 'flex', justifyContent: 'center', padding: 16 }}><div className="spinner" /></div>
          ) : orgs.length === 0 ? (
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', padding: 8, textAlign: 'center' }}>No orgs found.</div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {orgs.map(o => (
                <div key={o.name} className="org-list-row" style={{ display: 'flex', alignItems: 'center' }}>
                <button
                  onClick={() => selectOrg(o.name)}
                  style={{
                    flex: 1, minWidth: 0,
                    display: 'flex', alignItems: 'center', gap: 6, textAlign: 'left',
                    background: selected === o.name ? 'var(--elevated)' : 'transparent',
                    border: selected === o.name ? '1px solid var(--border-active)' : '1px solid transparent',
                    borderRadius: 'var(--radius)', padding: '6px 8px', cursor: 'pointer',
                  }}
                >
                  <StatusDot status={o.status} />
                  <span style={{ flex: 1, minWidth: 0, fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{o.name}</span>
                  {o.kind === 'holding' && <Boxes size={10} style={{ color: 'var(--text-muted)', flexShrink: 0 }} aria-label="holding org" />}
                  <Badge count={needsYouCounts[o.name] || 0} style={{ flexShrink: 0 }} />
                </button>
                <OpenAsBubbleButton orgName={o.name} compact />
                </div>
              ))}
            </div>
          )}
          </div>
        </div>
        ) : (
          <button
            onClick={() => setOrgListOpen(true)}
            title="Show org list"
            style={panelStripStyle('right')}
          >
            <ChevronRight size={13} />
          </button>
        )
        )}

        {/* Detail */}
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
          {notInitialized ? (
            <MonomindInitPrompt onInitialized={() => loadOrgs()} />
          ) : !selected ? (
            <div className="empty-state" style={{ flex: 1 }}>
              <div className="empty-state-icon"><Building2 size={30} /></div>
              <div className="empty-state-title">Select an org</div>
              <div className="empty-state-desc">Pick an org from the list to see its status, questions, and gates.</div>
            </div>
          ) : (
            <>
              {!(designerFullscreen && tab === 'design') && (
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '6px 10px', borderBottom: '1px solid var(--border)', flexWrap: 'wrap', flexShrink: 0 }}>
                <Building2 size={12} style={{ color: 'var(--text-muted)' }} />
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11.5, fontWeight: 600, color: 'var(--text)' }}>{selected}</span>
                {isHolding && <Chip>holding</Chip>}
                <PublishOrgButton orgName={selected} />
                <OpenAsBubbleButton orgName={selected} />
                {/* flex-basis 0 keeps the bar beside the name and lets it wrap
                    its own second line (the jev note) instead of the header's. */}
                <div style={{ flex: '1 1 0', display: 'flex', justifyContent: 'flex-end' }}>
                  <AutonomyBar orgName={selected} onOpenJevSettings={onNavigate ? openJevSettings : undefined} />
                </div>
              </div>
              )}
              {/* The designer shows its own, refreshed with each edit. */}
              {tab !== 'design' && <OrgSignatureBanner orgName={selected} />}
              {!(designerFullscreen && tab === 'design') && (
              <div style={{ display: 'flex', gap: 4, padding: '8px', borderBottom: '1px solid var(--border)', overflowX: 'auto', flexShrink: 0 }}>
                {TABS.filter(tabDef => !tabDef.holdingOnly || isHolding).map(tabDef => {
                  const Icon = tabDef.icon
                  const active = tab === tabDef.id
                  const badgeCount = tabDef.id === 'needs' ? (needsYouCounts[selected] || 0)
                    : tabDef.id === 'queued' ? queuedCount : 0
                  // Needs you is waiting on the person; a queued message
                  // only waits for the org to start, so it is not red.
                  const badgeBg = tabDef.id === 'queued' ? 'var(--text-muted)' : 'var(--red, #ef4444)'
                  const badgeFg = tabDef.id === 'queued' ? 'var(--surface, #060b13)' : '#fff'
                  return (
                    <button
                      key={tabDef.id}
                      onClick={() => setTab(tabDef.id)}
                      style={{
                        position: 'relative',
                        display: 'flex', alignItems: 'center', gap: 4, whiteSpace: 'nowrap',
                        fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 'var(--radius)',
                        background: active ? 'var(--elevated)' : 'transparent',
                        border: active ? '1px solid var(--border-active)' : '1px solid transparent',
                        color: active ? 'var(--text)' : 'var(--text-muted)', cursor: 'pointer',
                      }}
                    >
                      <Icon size={11} /> {t(tabDef.labelKey)}
                      {badgeCount > 0 && (
                        <span style={{
                          position: 'absolute', top: -5, right: -5,
                          minWidth: 14, height: 14, padding: '0 3px', borderRadius: 7,
                          background: badgeBg, color: badgeFg,
                          fontFamily: 'var(--font-mono)', fontSize: 9, fontWeight: 700, lineHeight: '14px', textAlign: 'center',
                          boxShadow: '0 0 0 2px var(--surface, #060b13)',
                        }}>
                          {badgeCount > 99 ? '99+' : badgeCount}
                        </span>
                      )}
                    </button>
                  )
                })}
                {runningOrgs.has(selected) ? (
                  <button
                    disabled
                    style={{
                      marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 4,
                      fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 'var(--radius)',
                      background: 'transparent', border: '1px solid var(--border)', color: 'var(--text-muted)', cursor: 'default',
                    }}
                  >
                    <Loader2 size={11} style={{ animation: 'spin 0.9s linear infinite' }} /> {t('orgs.tabBar.running')}
                  </button>
                ) : (
                  <button
                    onClick={() => setRunPromptOpen(v => !v)}
                    title={t('orgs.tabBar.runTitle')}
                    style={{
                      marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 4,
                      fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 'var(--radius)',
                      background: runPromptOpen ? 'var(--elevated)' : 'transparent',
                      border: runPromptOpen ? '1px solid var(--border-active)' : '1px solid var(--border)',
                      color: 'var(--text)', cursor: 'pointer',
                    }}
                  >
                    <Play size={11} /> {t('orgs.tabBar.run')}
                  </button>
                )}
                {tab === 'design' && (
                  <button
                    onClick={() => setDesignerFullscreen(v => !v)}
                    title={designerFullscreen ? t('orgs.tabBar.exitFullscreen') : t('orgs.tabBar.fullscreen')}
                    style={{
                      display: 'flex', alignItems: 'center', gap: 4,
                      fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 'var(--radius)',
                      background: 'transparent', border: '1px solid transparent', color: 'var(--text-muted)', cursor: 'pointer',
                    }}
                  >
                    <Maximize2 size={11} />
                  </button>
                )}
              </div>
              )}

              {runPromptOpen && (
                <div style={{ display: 'flex', gap: 6, padding: '6px 8px', borderBottom: '1px solid var(--border)', flexShrink: 0 }}>
                  <input
                    autoFocus
                    type="text"
                    value={runTaskText}
                    onChange={e => setRunTaskText(e.target.value)}
                    onKeyDown={e => { if (e.key === 'Enter') handleRunOrg(); if (e.key === 'Escape') { setRunPromptOpen(false); setRunTaskText('') } }}
                    placeholder="First message for the org (optional) — Enter to run"
                    style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 11, background: 'var(--elevated)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '6px 8px', outline: 'none' }}
                  />
                  <button className="btn btn-primary btn-sm" onClick={handleRunOrg}>
                    <Play size={11} /> Run
                  </button>
                </div>
              )}

              {!['design', 'needs', 'queued', 'group'].includes(tab) && (runningOrgs.has(selected) || runsList.length > 0) && (
                // Shared run context — which run every tab (not just
                // Overview) is scoped to. Logs/Costs/Flow/Decisions all read
                // selectedRun too now, so this needs to be visible no matter
                // which tab is open, not buried inside Overview's own body.
                <div style={{ display: 'flex', flexDirection: 'column', gap: 8, padding: '8px 12px', borderBottom: '1px solid var(--border)', flexShrink: 0 }}>
                  {runningOrgs.has(selected) && (
                    <div style={{
                      display: 'flex', alignItems: 'center', gap: 6, padding: '6px 10px',
                      background: 'rgba(0,245,212,0.06)', border: '1px solid rgba(0,245,212,0.2)', borderRadius: 'var(--radius)',
                    }}>
                      <span className="live-dot" />
                      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--teal, #00f5d4)', fontWeight: 600, letterSpacing: 0.5 }}>Org is running</span>
                    </div>
                  )}
                  {runsList.length > 0 && (
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>Run</span>
                      <select
                        value={selectedRun || ''}
                        onChange={e => setSelectedRun(e.target.value || null)}
                        className="filter-select"
                        style={{ flex: 1, maxWidth: 360 }}
                      >
                        {runsList.some(r => r.live) && <option value="live">Live (current run)</option>}
                        {runsList.filter(r => !r.live).map(r => {
                          const outcome = r.summary?.outcome?.status
                          return <option key={r.run} value={r.run}>{r.run}{outcome ? ` — ${outcome}` : ''}</option>
                        })}
                      </select>
                    </div>
                  )}
                </div>
              )}

              {tab === 'design' ? (
                // Full-bleed canvas — owns its own scrolling/panning, so it
                // deliberately sits outside the generic padded/overflow-auto
                // container every other tab uses.
                <div style={{ flex: 1, minHeight: 0, display: 'flex', position: 'relative' }}>
                  <OrgDesigner
                    orgName={selected}
                    fullscreen={designerFullscreen}
                    onToggleFullscreen={() => setDesignerFullscreen(v => !v)}
                    onOpenWorkflow={onNavigate ? (workflowId) => { if (workflowId) onNavigate('noderunner', { workflowId }) } : undefined}
                    onCreateWorkflow={onNavigate ? () => onNavigate('noderunner') : undefined}
                  />
                </div>
              ) : (
              <div style={{ flex: 1, overflowY: 'auto', padding: 12, display: 'flex', flexDirection: 'column', gap: 10 }}>
                {tabLoading && <div style={{ display: 'flex', justifyContent: 'center', padding: 8 }}><div className="spinner" /></div>}

                {!tabLoading && tab === 'overview' && (
                  <>
                    <FullAccessSummary orgName={selected} status={data.overview} onChanged={() => loadTab(selected, 'overview')} />
                    {!selectedRun ? (
                      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No completed runs yet.</div>
                    ) : selectedRun === 'live' ? (
                      <>
                        <OutcomeBanner
                          outcome={runsList.find(r => r.live)?.summary?.outcome}
                          finalMessage={extractFinalMessage(events, runsList.find(r => r.live)?.summary?.outcome)}
                        />
                        <SectionBudgetPanel orgName={selected} live />
                        <details>
                          <summary style={{ cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>Raw status</summary>
                          <Card><KVBlock obj={data.overview} /></Card>
                        </details>
                        <div>
                          <OrgToolActivity events={events} isLive runKey="live" />
                          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 6 }}>Live events</div>
                          <div style={{ display: 'flex', flexDirection: 'column', gap: 4, maxHeight: 260, overflowY: 'auto' }}>
                            {events.length === 0 && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>Waiting for events…</div>}
                            {events.filter(e => !isToolActivity(e)).map((e, i) => (
                              <div key={e.id ?? `${e.ts ?? ''}-${i}`} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '4px 8px', overflowWrap: 'anywhere' }}>
                                <span style={{ color: 'var(--cyan)' }}>{e.type}</span>{e.from ? ` ${e.from}→${e.to || '?'}` : ''}{e.subject ? `: ${e.subject}` : ''}{e.msg ? ` ${e.msg}` : ''}
                              </div>
                            ))}
                          </div>
                        </div>
                      </>
                    ) : (
                      // A specific historical run is selected — its own
                      // static report + logs, not the live tail above.
                      runDetail?.loading || runDetail?.run !== selectedRun ? (
                        <div style={{ display: 'flex', justifyContent: 'center', padding: 16 }}><div className="spinner" /></div>
                      ) : (
                        <>
                          <OutcomeBanner
                            outcome={runDetail.report?.outcome}
                            finalMessage={extractFinalMessage(runDetail.logs, runDetail.report?.outcome)}
                          />
                          <SectionBudgetPanel orgName={selected} run={selectedRun} />
                          <details>
                            <summary style={{ cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>Raw report</summary>
                            <Card><KVBlock obj={runDetail.report} /></Card>
                          </details>
                          <div>
                            <OrgToolActivity events={itemsOf(runDetail.logs)} runKey={selectedRun} />
                            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 6 }}>Events</div>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: 4, maxHeight: 260, overflowY: 'auto' }}>
                              {itemsOf(runDetail.logs).length === 0 && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>No events recorded.</div>}
                              {itemsOf(runDetail.logs).filter(e => !isToolActivity(e)).map((e, i) => (
                                <div key={e.id ?? `${e.ts ?? ''}-${i}`} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '4px 8px', overflowWrap: 'anywhere' }}>
                                  <span style={{ color: 'var(--cyan)' }}>{e.type}</span>{e.from ? ` ${e.from}→${e.to || '?'}` : ''}{e.subject ? `: ${e.subject}` : ''}{e.msg ? ` ${e.msg}` : ''}
                                </div>
                              ))}
                            </div>
                          </div>
                        </>
                      )
                    )}
                  </>
                )}

                {tab === 'needs' && (
                  <>
                    {(needsYouCounts[selected] || 0) > 0 && (
                      <button type="button" className="btn btn-primary btn-sm" data-testid="org-answer-in-bubble"
                        onClick={() => emitOpenOrgBubble(selected)} aria-label={t('orgBubble.answerInBubbleNamed', { name: selected })}
                        style={{ gap: 4, fontSize: 10, marginBottom: 8 }}>
                        <MessageCircle size={12} /> {t('orgBubble.answerInBubble')}
                      </button>
                    )}
                    <NeedsYouPanel orgName={selected} onCountChange={handleNeedsYouCount} />
                  </>
                )}

                {tab === 'queued' && (
                  <QueuedMessagesPanel orgName={selected} running={runningOrgs.has(selected)} onStartOrg={handleRunOrg} onCountChange={setQueuedCount} />
                )}

                {tab === 'decisions' && (
                  <DecisionsFeed orgName={selected} run={selectedRun && selectedRun !== 'live' ? selectedRun : ''} />
                )}

                {tab === 'documents' && (
                  <DocumentsPanel orgName={selected} run={selectedRun && selectedRun !== 'live' ? selectedRun : ''} live={selectedRun === 'live'} />
                )}

                {tab === 'group' && isHolding && (
                  <GroupView holding={selected} onOpenOrg={(name) => selectOrg(name)} />
                )}

                {tab === 'logs' && <ScheduleAuditPanel orgName={selected} live={selectedRun === 'live'} />}

                {!tabLoading && tab === 'logs' && (
                  itemsOf(data.logs).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No log entries.</div>
                    : <>
                      <OrgToolActivity events={itemsOf(data.logs)} isLive={selectedRun === 'live'} runKey={`logs-${selectedRun}`} />
                      {itemsOf(data.logs).filter(e => !isToolActivity(e)).map((e, i) => (
                      <div key={e.id ?? `${e.ts ?? ''}-${i}`} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '4px 8px' }}>
                        <span style={{ color: 'var(--cyan)' }}>{e.type}</span>{e.from ? ` ${e.from}→${e.to || '?'}` : ''}{e.subject ? `: ${e.subject}` : ''}
                      </div>
                    ))}
                    </>
                )}

                {!tabLoading && tab === 'costs' && (
                  <>
                    {itemsOf(data.costs).length === 0
                      ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No cost data.</div>
                      : itemsOf(data.costs).map((c, i) => (
                        <Card key={i}><KVBlock obj={c} /></Card>
                      ))}
                    {data.costs?.totals && <Card><KVBlock obj={data.costs.totals} /></Card>}
                  </>
                )}

                {!tabLoading && tab === 'flow' && (
                  itemsOf(data.flow?.edges).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No flow data.</div>
                    : itemsOf(data.flow?.edges).map((e, i) => (
                      <div key={i} style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '6px 10px' }}>
                        {e.from} → {e.to}{e.subject ? `: ${e.subject}` : ''}
                      </div>
                    ))
                )}

                {!tabLoading && tab === 'trace' && (
                  itemsOf(data.trace).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No decision trace recorded.</div>
                    : itemsOf(data.trace).map((d, i) => (
                      <Card key={i}><KVBlock obj={d} /></Card>
                    ))
                )}
              </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

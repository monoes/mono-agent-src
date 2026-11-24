import { useState, useEffect, useCallback, useRef } from 'react'
import { useMountedTimers } from '../lib/useMountedTimers.js'
import { X, RefreshCw, Building2 } from 'lucide-react'
import { api, onOrgEvent, onOrgEventsClosed, onOrgDesignUpdated, onOrgRunStatus, notify } from '../services/api.js'
import OrgDesigner from './orgdesigner/OrgDesigner.jsx'
import { acquireOrgEvents, orgEventHolders, restartOrgEvents } from '../lib/orgEventStreams.js'
import MonomindInitPrompt from './MonomindInitPrompt.jsx'
import useNeedsYouCounts from './orgs/useNeedsYouCounts.js'
import useQueuedCount from './orgs/useQueuedCount.js'
import { isSignatureRefusal, requestSignatureRefresh } from './orgs/OrgSignatureBanner.jsx'
import { itemsOf } from './orgs/orgsPanelParts.jsx'
import useOrgList from './orgs/useOrgList.js'
import OrgRail from './orgs/OrgRail.jsx'
import OrgDetailHeader from './orgs/OrgDetailHeader.jsx'
import OrgTabBody from './orgs/OrgTabBody.jsx'


export default function OrgsPanel({ embedded = false, isOpen = true, onClose, pageActive = true, onNavigate, pendingSelectOrgName, onConsumePendingSelect }) {
  const timers = useMountedTimers()
  // Effective openness: an embedded panel is open when its isOpen prop says
  // so; a page-mode panel (rendered by the Orgs page) stays mounted under
  // App.jsx's keep-alive navigation, so for it "open" additionally requires
  // the Orgs page to be the active tab — otherwise its SSE stream would
  // keep running (and the event tail growing) forever after the user
  // navigated away (FV4-10).
  const effectiveOpen = embedded ? isOpen : pageActive
  const [selected, setSelected] = useState(null) // org name
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

  const { orgs, loadingOrgs, notInitialized, loadOrgs } = useOrgList(effectiveOpen)

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
        <OrgRail embedded={embedded} orgListOpen={orgListOpen} setOrgListOpen={setOrgListOpen} creatingOrg={creatingOrg} setCreatingOrg={setCreatingOrg} newOrgName={newOrgName} setNewOrgName={setNewOrgName} newOrgGoal={newOrgGoal} setNewOrgGoal={setNewOrgGoal} createBusy={createBusy} createError={createError} setCreateError={setCreateError} handleCreateOrg={handleCreateOrg} loadOrgs={loadOrgs} selectOrg={selectOrg} loadingOrgs={loadingOrgs} orgs={orgs} selected={selected} needsYouCounts={needsYouCounts} />
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
              <OrgDetailHeader selected={selected} tab={tab} setTab={setTab} designerFullscreen={designerFullscreen} setDesignerFullscreen={setDesignerFullscreen} isHolding={isHolding} onNavigate={onNavigate} openJevSettings={openJevSettings} needsYouCounts={needsYouCounts} queuedCount={queuedCount} runningOrgs={runningOrgs} runPromptOpen={runPromptOpen} setRunPromptOpen={setRunPromptOpen} runTaskText={runTaskText} setRunTaskText={setRunTaskText} handleRunOrg={handleRunOrg} runsList={runsList} selectedRun={selectedRun} setSelectedRun={setSelectedRun} />

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
              <OrgTabBody selected={selected} tab={tab} tabLoading={tabLoading} data={data} events={events} runsList={runsList} selectedRun={selectedRun} runDetail={runDetail} needsYouCounts={needsYouCounts} handleNeedsYouCount={handleNeedsYouCount} runningOrgs={runningOrgs} handleRunOrg={handleRunOrg} setQueuedCount={setQueuedCount} isHolding={isHolding} selectOrg={selectOrg} loadTab={loadTab} />
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

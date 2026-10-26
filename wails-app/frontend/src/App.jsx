import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import Sidebar from './components/Sidebar.jsx'
import { usePendingCount } from './lib/usePendingCount.js'
import StatusBar from './components/StatusBar.jsx'
import { startBackgroundHealth, subscribeHealth, summarize, isFixing } from './lib/health.js'
import Toasts from './components/Toasts.jsx'
import ErrorBoundary from './components/ErrorBoundary.jsx'
import ConfirmHost from './components/ConfirmDialog.jsx'
import CoderBubbles from './components/bubbles/CoderBubbles.jsx'
import { useCoderBubbles } from './components/bubbles/useCoderBubbles.js'
import { CoderBubbleContext } from './components/bubbles/CoderBubbleContext.jsx'
import HistoryPanel from './components/history/HistoryPanel.jsx'
import { useOrgBubbleWatch } from './components/bubbles/useOrgBubbleWatch.js'
import HumanInLoop from './pages/HumanInLoop.jsx'
import Dashboard from './pages/Dashboard.jsx'
import People from './pages/People.jsx'
import Profile from './pages/Profile.jsx'
import PostDetail from './pages/PostDetail.jsx'
import Connections from './pages/Connections.jsx'
import Communications from './pages/Communications.jsx'
import Publication from './pages/Publication.jsx'
import Agents from './pages/Agents.jsx'
import Orgs from './pages/Orgs.jsx'
import Logs from './pages/Logs.jsx'
import NodeRunner from './pages/NodeRunner.jsx'
import SettingsPage from './pages/Settings.jsx'
import ImageVault from './pages/ImageVault.jsx'
import Vault from './pages/Vault.jsx'
import Applications from './pages/Applications.jsx'
import Documents from './pages/Documents.jsx'
import Info from './pages/Info.jsx'
import Tasks from './pages/Tasks.jsx'
import FileViewerModal, { fileViewerKind } from './components/FileViewerModal.jsx'
import * as WailsApp from './wailsjs/go/main/App'
import { api, notify, onLogEntry, onOrgDesignUpdated, subscribeEvent } from './services/api.js'
import { isAgentNotSetup } from './lib/agentSetup.js'

// Mirrors Documents.jsx's own cap (the backend GetProfileDocumentData limit)
// — kept as a local literal there too, so duplicating it here rather than
// exporting a shared constant for one other call site.
const maxInlinePreviewBytes = 25 * 1024 * 1024

// banners: the account notices AccountApp hands the app to wear (components/account).
export default function App({ banners = null }) {
  const [activePage, setActivePage] = useState('dashboard')
  const [navData, setNavData] = useState(null) // extra data passed to a page (e.g., executionId)
  const [profileId, setProfileId] = useState(null)
  const [postId, setPostId] = useState(null)
  const [dbConnected, setDbConnected] = useState(false)
  const [stats, setStats] = useState(null)
  const [logs, setLogs] = useState([])
  const [peopleRefreshKey, setPeopleRefreshKey] = useState(0)
  // Pages mount lazily on first visit, then stay mounted forever after —
  // avoids paying every page's initial data-fetch cost at app startup while
  // still keeping state alive once a page has actually been opened.
  const [visitedPages, setVisitedPages] = useState(() => new Set(['dashboard']))
  const [globalHilOpen, setGlobalHilOpen] = useState(false)
  const [globalHilCount, setGlobalHilCount] = useState(0) // reported by the open drawer (incl. org items)
  const { count: pendingCount } = usePendingCount()     // Jev-free summary count, drawer closed
  // The document a chat result artifact card asked to open (Task 6) — a
  // separate instance from Documents.jsx's own viewingDoc, since that page
  // may not even be mounted yet (persistentPages only mounts a page once
  // visited) and this must work regardless of which page is active.
  const [viewingArtifactDoc, setViewingArtifactDoc] = useState(null)

  // Coder chats open as floating bubbles (#227), not in the assistant panel.
  const coderBubbles = useCoderBubbles()
  // Running orgs as bubbles (#229): opened from the Orgs page, or on their
  // own when an org asks the person something.
  const onCloseOrgBubble = useOrgBubbleWatch(coderBubbles)
  const { openConversation: openCoderBubble, openDraft: openCoderDraft, openNewChat } = coderBubbles
  const openCoderChat = useCallback((conv) => {
    if (conv) openCoderBubble(conv)
    else openCoderDraft()
  }, [openCoderBubble, openCoderDraft])

  const openProfile = useCallback((id) => {
    setProfileId(id)
    setActivePage('profile')
  }, [])

  const closeProfile = useCallback(() => {
    setActivePage('people')
    setProfileId(null)
  }, [])

  const openPost = useCallback((id) => {
    setPostId(id)
    setActivePage('postDetail')
  }, [])

  const closePost = useCallback(() => {
    setPostId(null)
    setActivePage('profile')
  }, [])

  const navigate = useCallback((page, data) => {
    if (page !== 'postDetail') setPostId(null)
    if (page !== 'profile' && page !== 'postDetail') setProfileId(null)
    setNavData(data || null)
    // A link to a person (an ask's recipient) opens their profile.
    if (page === 'people' && data?.person) { setProfileId(data.person); setActivePage('profile'); return }
    if (page === 'orgs' && data?.org) setPendingOrgSelect({ name: data.org, tab: data.tab || 'overview', run: data.run })
    setActivePage(page)
  }, [])

  // Marks whatever page is active as visited — deliberately keyed off
  // activePage itself (not routed through navigate()) so it also catches
  // the couple of places that call setActivePage directly (Settings'
  // onNavigate, closeProfile) instead of the navigate() wrapper.
  useEffect(() => {
    setVisitedPages(prev => (prev.has(activePage) ? prev : new Set(prev).add(activePage)))
  }, [activePage])

  // System health: check on start and every 30 minutes (lib/health.js).
  // The first time a check this session finds setup unfinished (a required
  // item missing, or no monoagentcli at all), open Settings where the
  // System health section can finish it.
  useEffect(() => {
    const stop = startBackgroundHealth()
    let opened = false
    const off = subscribeHealth(h => {
      if (opened || (!h.report && !h.cliMissing)) return
      opened = true
      if (h.cliMissing || summarize(h.report).level === 'broken') navigate('settings')
    })
    return () => { off(); stop() }
  }, [navigate])

  // Bring the user to a newly-created org even if they've never visited
  // Orgs this session. Pages below only render/mount once visitedPages
  // has seen their id (see persistentPages filter further down) — OrgsPanel
  // has its own onOrgDesignUpdated listener that auto-selects a new org,
  // but that listener doesn't exist until OrgsPanel has mounted at least
  // once, which never happens if the user went straight to Chat and asked
  // it to build an org there. This top-level listener is always mounted,
  // so it's the only thing that can react the very first time. It tracks
  // known org names itself (OrgsPanel's own rail state isn't reachable
  // from here) and hands off the name to select via pendingOrgSelect,
  // which Orgs/OrgsPanel consumes once mounted (including on this exact
  // mount, triggered by the navigate() call below).
  const knownOrgNamesRef = useRef(null) // null = not yet loaded
  const [pendingOrgSelect, setPendingOrgSelect] = useState(null)
  useEffect(() => {
    api.listOrgDesigns().then(res => {
      const items = Array.isArray(res) ? res : (res?.items || [])
      knownOrgNamesRef.current = new Set(items.map(o => o.name))
    }).catch(() => { knownOrgNamesRef.current = new Set() })
    const off = onOrgDesignUpdated((payload) => {
      if (!payload?.orgName || payload.deleted) return
      if (knownOrgNamesRef.current?.has(payload.orgName)) return
      knownOrgNamesRef.current?.add(payload.orgName)
      // A System health fix (monomind init) can create a sample org; don't
      // pull the user out of Settings while it runs.
      if (isFixing()) return
      setPendingOrgSelect(payload.orgName)
      navigate('orgs')
    })
    return off
  }, [navigate])

  // Chat result artifact cards (Task 6) call this to open what a tool call
  // actually produced — never a router, never a new navigation concept:
  // org reuses the exact pendingOrgSelect+navigate('orgs') mechanism the
  // watcher above already uses (the card only ever gets an org name that
  // chatArtifacts.resolveArtifact already confirmed against the real org
  // listing, so this is exactly as safe as the watcher's own usage), and
  // document opens the same FileViewerModal Documents.jsx uses, in its own
  // App-level instance so it works regardless of which page is active.
  // Workflow has no "open" case: there's no workflow editor route to send
  // it to (plan: "Metadata/Copy ID" — the card itself just copies the id),
  // so onOpenArtifact is never even called for that artifact type.
  const onOpenArtifact = useCallback((artifact) => {
    if (artifact.type === 'org') {
      knownOrgNamesRef.current?.add(artifact.name)
      setPendingOrgSelect(artifact.name)
      navigate('orgs')
    } else if (artifact.type === 'document') {
      // Same in-app-preview-or-hand-to-OS split Documents.jsx's own
      // handleOpenDocument uses — FileViewerModal has no supported renderer
      // for every extension docscan/save_document can produce (e.g. .docx),
      // and its own doc comment says a caller must screen for that before
      // opening it, or it's stuck on "Loading…" forever with no fetch ever
      // issued.
      if (fileViewerKind(artifact.filename) && artifact.sizeBytes <= maxInlinePreviewBytes) {
        setViewingArtifactDoc({ id: artifact.id, filename: artifact.filename, path: artifact.path, size_bytes: artifact.sizeBytes })
        return
      }
      const reason = artifact.sizeBytes > maxInlinePreviewBytes
        ? `"${artifact.filename}" is too large to preview in-app`
        : `No built-in viewer for ${artifact.filename.split('.').pop()?.toUpperCase() || 'this'} files`
      notify('open', `${reason} — opening "${artifact.filename}" with your system's default application.`)
      WailsApp.OpenPathWithOS(artifact.path).catch(e => notify('open', `Could not open "${artifact.filename}": ${e}`))
    } else if (artifact.type === 'image') {
      navigate('vault')
    }
  }, [navigate])

  // Initial data load
  useEffect(() => {
    const checkDB = async () => {
      const connected = await api.isDBConnected()
      setDbConnected(!!connected)
    }
    const loadStats = async () => {
      const s = await api.getDashboardStats()
      if (s) setStats(s)
    }
    const loadLogs = async () => {
      const l = await api.getLogs()
      if (l) setLogs(l)
    }
    checkDB()
    loadStats()
    loadLogs()
  }, [])

  // Exposed to Logs.jsx's Refresh button — re-fetches the full log buffer
  // from the backend (in addition to the live event stream below, which can
  // miss entries if the app was backgrounded).
  const refreshLogs = useCallback(async () => {
    const l = await api.getLogs()
    if (l) setLogs(l)
  }, [])

  // Live log streaming
  useEffect(() => {
    const off = onLogEntry((entry) => {
      setLogs(prev => {
        const next = [...prev, entry]
        return next.length > 500 ? next.slice(-500) : next
      })
    })
    return off
  }, [])

  // Workflow-run completion refresh
  useEffect(() => {
    const off = subscribeEvent('workflow:complete', async (data) => {
      // A run that failed because the AI agent is not set up says so, with
      // the link to the AI agents page (Toasts).
      if (data?.success === false && isAgentNotSetup(data?.code)) notify('run workflow', data.error || '', data.code)
      const s = await api.getDashboardStats()
      if (s) setStats(s)
      setPeopleRefreshKey(k => k + 1)
    })
    return off
  }, [])

  const refreshStats = useCallback(async () => {
    const s = await api.getDashboardStats()
    if (s) setStats(s)
  }, [])

  // These pages stay mounted for the lifetime of the app once first
  // visited — switching tabs only toggles CSS visibility, it never
  // unmounts them — so per-page state (scanned runtimes, open chat panel,
  // selected org, canvas edits, filters, scroll position, …) survives
  // navigating away and back. Without this, every tab switch destroyed and
  // recreated the page component from scratch, discarding all local state
  // and — for pages like Agents/Orgs that fetch on mount — re-running
  // their (multi-second) initial data load every single time.
  const persistentPages = {
    dashboard: <Dashboard isActive={activePage === 'dashboard'} onRefresh={refreshStats} onNavigate={navigate} onOpenHil={() => setGlobalHilOpen(true)} />,
    noderunner: <NodeRunner onNavigate={navigate} navData={navData} onWorkflowsChanged={refreshStats} />,
    people:    <People key={peopleRefreshKey} isActive={activePage === 'people'} onProfile={openProfile} onNavigate={navigate} />,
    communications: <Communications isActive={activePage === 'communications'} onProfile={openProfile} navData={activePage === 'communications' ? navData : null} onOpenHil={() => setGlobalHilOpen(true)} />,
    publication: <Publication isActive={activePage === 'publication'} onNavigate={navigate} />,
    connections: <Connections onRefresh={refreshStats} navData={activePage === 'connections' ? navData : null} />,
    vault: <ImageVault isActive={activePage === 'vault'} />,
    secretsVault: <Vault isActive={activePage === 'secretsVault'} />,
    applications: <Applications isActive={activePage === 'applications'} navData={activePage === 'applications' ? navData : null} onNavigate={navigate} onOpenCoderChat={openCoderChat} />,
    documents: <Documents isActive={activePage === 'documents'} />,
    info: <Info isActive={activePage === 'info'} />,
    tasks: <Tasks isActive={activePage === 'tasks'} navData={activePage === 'tasks' ? navData : null} />,
    ai: <Agents />,
    orgs: <Orgs isActive={activePage === 'orgs'} onNavigate={navigate} pendingSelectOrgName={pendingOrgSelect} onConsumePendingSelect={() => setPendingOrgSelect(null)} />,
    logs:      <Logs logs={logs} onClear={() => { api.clearLogs(); setLogs([]) }} onRefresh={refreshLogs} />,
    settings:  <SettingsPage onNavigate={navigate} navData={activePage === 'settings' ? navData : null} isActive={activePage === 'settings'} />,
  }

  // Detail views keyed by a changing id (which profile/post) — these SHOULD
  // reset when you open a different record, so they stay conditionally
  // rendered rather than kept alive like the tab pages above.
  const detailPages = {
    profile:   <Profile id={profileId} onBack={closeProfile} onOpenURL={api.openURL} onOpenPost={openPost} />,
    postDetail: <PostDetail id={postId} onBack={closePost} onOpenURL={api.openURL} />,
  }

  // Any page's Send-to-coder button reaches the bubbles through this (#957).
  const coderBubbleContext = useMemo(() => ({ openDraft: openCoderDraft, openNewChat, navigate }), [openCoderDraft, openNewChat, navigate])

  const isDetailPage = activePage in detailPages
  // Guard against an unrecognized activePage (e.g. a stale/typo'd nav
  // target) — fall back to showing dashboard rather than a blank screen.
  const effectiveActivePage = (activePage in persistentPages || isDetailPage) ? activePage : 'dashboard'

  return (
    <CoderBubbleContext.Provider value={coderBubbleContext}>
    <div className="app-layout">
      <Sidebar
        activePage={activePage}
        onNavigate={navigate}
        stats={stats}
        dbConnected={dbConnected}
      />
      <div style={{ display: 'flex', flexDirection: 'row', minWidth: 0, minHeight: 0, overflow: 'hidden' }}>
        <main className="main-content">
          {banners}
          <ErrorBoundary>
            {Object.entries(persistentPages)
              .filter(([id]) => visitedPages.has(id))
              .map(([id, el]) => (
                <div
                  key={id}
                  style={{
                    display: effectiveActivePage === id ? 'flex' : 'none',
                    flexDirection: 'column',
                    height: '100%',
                    overflow: 'hidden',
                  }}
                >
                  {el}
                </div>
              ))}
            {isDetailPage && detailPages[activePage]}
          </ErrorBoundary>
        </main>

        {/* Global Human in Loop — central review and approval queue accessible from anywhere */}
        <HumanInLoop
          embedded
          isOpen={globalHilOpen}
          onClose={() => setGlobalHilOpen(false)}
          onPendingCountChange={setGlobalHilCount}
          onProfile={openProfile}
          onNavigate={navigate}
        />
      </div>

      {/* The new-chat and human-in-loop buttons live inside StatusBar itself
          (bottom bar, far right) — small icons there rather than separate
          floating overlay buttons, reachable from anywhere in the app. */}
      <StatusBar
        stats={stats}
        dbConnected={dbConnected}
        hilOpen={globalHilOpen}
        hilCount={globalHilOpen ? globalHilCount : pendingCount}
        onToggleHil={() => setGlobalHilOpen(v => !v)}
        onOpenHealth={() => navigate('settings')}
      />
      <CoderBubbles store={coderBubbles} onNavigate={navigate} onCloseOrg={onCloseOrgBubble} onOpenArtifact={onOpenArtifact} />
      <HistoryPanel store={coderBubbles} onNavigate={navigate} />
      <Toasts onNavigate={navigate} />
      <ConfirmHost />
      {viewingArtifactDoc && (
        <FileViewerModal doc={viewingArtifactDoc} onClose={() => setViewingArtifactDoc(null)} />
      )}
    </div>
    </CoderBubbleContext.Provider>
  )
}

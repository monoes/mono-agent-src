// Connections page (spec §7): two sections. Browser Automations are the
// installed automation packages (`automation list --json`), each with its
// browser login; API Connections are the registry platforms that can be
// connected without the browser. A site with both appears in both.
import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import i18nOr from '../lib/i18nOr.js' // for messages built in callbacks
import { RefreshCw, Upload, Circle } from 'lucide-react'
import { api } from '../services/api.js'
import { emitAutomationsChanged } from '../lib/appEvents.js'
import BrowserAutomations, { RecordHelpDialog } from './connections/BrowserAutomations.jsx'
import LibraryModal from '../components/library/LibraryModal.jsx'
import ApiConnections, { resolveConn } from './connections/ApiConnections.jsx'
import ApiConnectionModal from './connections/ApiConnectionModal.jsx'
import AutomationDrawer from './connections/AutomationDrawer.jsx'
import ImportDialog from './connections/ImportDialog.jsx'

// navData (from the dashboard) may name an automation and a drawer tab:
// { automationId, tab: 'health' | 'recordings' | … }.
const DRAWER_TABS = { overview: 'Overview', session: 'Session', actions: 'Actions', health: 'Health', recordings: 'Recordings' }

export default function Connections({ onRefresh, navData }) {
  const { t } = useTranslation()
  const [platforms,    setPlatforms]    = useState([])
  const [connections,  setConnections]  = useState([])
  const [automations,  setAutomations]  = useState([])
  const [autoError,    setAutoError]    = useState('')
  const [loading,      setLoading]      = useState(true)
  const [error,        setError]        = useState(null)
  const [selected,     setSelected]     = useState(null)
  const [openAuto,     setOpenAuto]     = useState(null)
  const [drawerTab,    setDrawerTab]    = useState('Overview')
  const [importing,    setImporting]    = useState(false)
  const [recordHelp,   setRecordHelp]   = useState(false)
  const [libraryOpen,  setLibraryOpen]  = useState(false)
  const [pollFailed, setPollFailed] = useState(false)
  const pollRef = useRef(null)

  const loadAutomations = useCallback(async () => {
    const res = await api.listAutomations()
    if (!res || res.error) { setAutoError(res?.error || i18nOr('ui6Conn.load_automations_failed')); return }
    setAutoError('')
    setAutomations(Array.isArray(res.automations) ? res.automations : [])
  }, [])

  const loadAll = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      if (!silent) setError(null)
      const [plats, conns] = await Promise.all([
        api.listPlatforms(''),
        api.listConnections(''),
        loadAutomations(),
      ])
      setPlatforms(Array.isArray(plats) ? plats : [])
      setConnections(Array.isArray(conns) ? conns : [])
      setPollFailed(false)
    } catch (e) {
      if (!silent) setError(e?.message || i18nOr('ui6Conn.load_connections_failed'))
      else setPollFailed(true) // keep the stale rows but say they may be out of date
    } finally {
      if (!silent) setLoading(false)
    }
  }, [loadAutomations])

  useEffect(() => { loadAll() }, [loadAll])

  useEffect(() => {
    if (selected) {
      pollRef.current = setInterval(() => loadAll(true), 10000)
    } else {
      clearInterval(pollRef.current)
    }
    return () => clearInterval(pollRef.current)
  }, [selected, loadAll])

  const handleRefresh = useCallback(async () => {
    await loadAll(true)
    onRefresh?.()
  }, [loadAll, onRefresh])

  const handleDisconnect = useCallback(async () => {
    await loadAll(true)
    onRefresh?.()
    setSelected(null)
  }, [loadAll, onRefresh])

  const closeDrawer = useCallback(() => { setOpenAuto(null); setDrawerTab('Overview') }, [])
  const openDrawer = useCallback(a => { setDrawerTab('Overview'); setOpenAuto(a) }, [])

  // Open the automation a deep link names once the list has it — once per
  // link: navData stays set while this page is showing, and the list reloads
  // often (drawer changes, Refresh), which must not reopen the drawer.
  const appliedLink = useRef(null)
  useEffect(() => {
    const id = navData?.automationId
    if (!id || appliedLink.current === navData || !automations.some(a => a.id === id)) return
    appliedLink.current = navData
    setDrawerTab(DRAWER_TABS[navData.tab] || 'Overview')
    setOpenAuto({ id })
  }, [navData, automations])

  // A package was installed, removed, restored, rolled back or gained a
  // saved action: reload the cards and tell the workflow editor, whose node
  // palette lists these packages' actions.
  const automationsChanged = useCallback(async () => {
    await loadAutomations()
    emitAutomationsChanged()
  }, [loadAutomations])

  return (
    <>
      <div className="page-header">
        <div className="page-header-left">
          <div className="page-title">{t('ui6Conn.page_title')}</div>
          <div className="page-subtitle">{loading ? t('ui6Conn.loading') : t('ui6Conn.page_subtitle')}</div>
        </div>
        <div className="page-header-right" style={{ display: 'flex', gap: 6 }}>
          <button className="btn btn-secondary btn-sm" onClick={() => setImporting(true)} style={{ gap: 5 }}><Upload size={12} /> {t('ui6Conn.import_automation')}</button>
          <button className="btn btn-secondary btn-sm" onClick={() => setRecordHelp(true)} style={{ gap: 5 }}><Circle size={12} color="var(--red)" /> {t('ui6Conn.record_new')}</button>
          <button className="btn btn-ghost btn-sm" onClick={() => loadAll()} style={{ gap: 5 }}><RefreshCw size={12} /> {t('ui6Conn.refresh')}</button>
        </div>
      </div>

      <div className="page-body">
        {pollFailed && !error && <div role="status" data-testid="conn-stale" style={{ marginBottom: 12, fontSize: 11, color: 'var(--text-muted)' }}>{t('connectionsPage.stale')}</div>}
        {error && <div style={{ padding: '12px 16px', background: 'rgba(239,68,68,.08)', border: '1px solid rgba(239,68,68,.2)', borderRadius: 'var(--radius)', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--red)', marginBottom: 12 }}>{error}</div>}
        {loading ? (
          <div className="empty-state"><div className="spinner" /></div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 36, paddingBottom: 24 }}>
            <BrowserAutomations
              automations={automations}
              error={autoError}
              onOpen={openDrawer}
              onRecord={() => setRecordHelp(true)}
              onImport={() => setImporting(true)}
              onLibrary={() => setLibraryOpen(true)}
            />
            <ApiConnections platforms={platforms} connections={connections} onSelect={setSelected} />
          </div>
        )}
      </div>

      {selected && (
        <ApiConnectionModal
          platform={selected}
          conn={resolveConn(selected, connections)}
          onClose={() => setSelected(null)}
          onRefresh={handleRefresh}
          onDisconnect={handleDisconnect}
        />
      )}
      {openAuto && (
        <AutomationDrawer
          key={`${openAuto.id}/${drawerTab}`}
          automation={automations.find(a => a.id === openAuto.id) || openAuto}
          initialTab={drawerTab}
          onClose={closeDrawer}
          onChanged={automationsChanged}
        />
      )}
      {importing && <ImportDialog installedPackages={automations} onClose={() => setImporting(false)} onInstalled={automationsChanged} />}
      {recordHelp && <RecordHelpDialog onClose={() => setRecordHelp(false)} />}
      {libraryOpen && <LibraryModal kind="automation" onClose={() => setLibraryOpen(false)} onInstalled={automationsChanged} />}
    </>
  )
}

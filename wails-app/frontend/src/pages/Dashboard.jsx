// The at-a-glance home. Every number comes from the CLI — `summary`,
// `org summary`, `workflow list` and `workflow executions --all` (see
// dashboard/useDashboardData.js); this page only lays the cards out.
import { useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw, GitBranch } from 'lucide-react'
import { api, reportRunFailure } from '../services/api.js'
import { GetVersion } from '../wailsjs/go/main/App'
import { getHealth, subscribeHealth, summarize } from '../lib/health.js'
import { useDashboardData } from './dashboard/useDashboardData.js'
import { attentionItems, unreadSections } from './dashboard/attention.js'
import AttentionStrip from './dashboard/AttentionStrip.jsx'
import StatRow from './dashboard/StatRow.jsx'
import WorkflowsCard from './dashboard/WorkflowsCard.jsx'
import RecentRunsCard from './dashboard/RecentRunsCard.jsx'
import ActivityCard from './dashboard/ActivityCard.jsx'
import SystemCard from './dashboard/SystemCard.jsx'
import OrgsCard from './dashboard/OrgsCard.jsx'
import AutomationsCard from './dashboard/AutomationsCard.jsx'
import AccountsCard from './dashboard/AccountsCard.jsx'
import { loadScope, saveScope, currentProfileId } from './dashboard/scope.js'
import ProfilesCard from './dashboard/ProfilesCard.jsx'
import GetStartedCard from './dashboard/GetStartedCard.jsx'
import { bridgeState, getStartedSteps, getStartedComplete, loadDismissed, okRunCount, saveDismissed } from './dashboard/getStarted.js'
import { AccountContext } from '../lib/accountContext.js'
import NewProfileModal from '../components/NewProfileModal.jsx'
import { switchToProfile } from './dashboard/profileSwitch.js'

const noGate = { subscribe: () => () => {}, getSnapshot: () => null }

export default function Dashboard({ isActive = true, onRefresh, onNavigate, onOpenHil }) {
  const { t } = useTranslation()
  const [scope, setScopeState] = useState(loadScope)
  const setScope = (s) => { saveScope(s); setScopeState(s) }
  const { summary, summaryFailed, noProfile, extension, orgs, workflows, executions, loading, refresh, setExecutions, reloadLists } = useDashboardData({ active: isActive, scope })
  const gate = useContext(AccountContext) || noGate
  const accountSnap = useSyncExternalStore(gate.subscribe, gate.getSnapshot)
  const bridge = bridgeState(summary?.services?.bridge, extension)
  const [getStartedHidden, setGetStartedHidden] = useState(loadDismissed)
  const steps = getStartedSteps({
    signedIn: !!accountSnap?.status?.user,
    okRuns: okRunCount(executions),
    bridge,
    daemonRunning: summary?.services?.daemon?.running,
  })
  const showGetStarted = scope === 'profile' && !loading && !getStartedHidden && !getStartedComplete(steps)
  const currentId = currentProfileId(summary)
  const [ver, setVer] = useState(null)
  const [newProfileOpen, setNewProfileOpen] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [health, setHealth] = useState(getHealth())
  useEffect(() => { GetVersion().then(setVer).catch(() => {}) }, [])
  useEffect(() => subscribeHealth(setHealth), [])
  const items = useMemo(
    () => attentionItems(summary, orgs, health?.report ? summarize(health.report) : null),
    [summary, orgs, health],
  )

  // Timers started here must not outlive the page (it can be hidden mid-run).
  const timers = useRef(new Set())
  useEffect(() => () => { timers.current.forEach(clearTimeout); timers.current.clear() }, [])
  const later = (fn, ms) => {
    const id = setTimeout(() => { timers.current.delete(id); fn() }, ms)
    timers.current.add(id)
  }
  const handleRefresh = async () => {
    setRefreshing(true)
    await Promise.all([onRefresh?.(), refresh()])
    later(() => setRefreshing(false), 400)
  }
  // Optimistic status flips so the row reacts at once; the next poll corrects it.
  const handleRun = async (id) => {
    setExecutions(prev => prev.map(e => (e.workflow_id === id && e.status !== 'RUNNING' ? { ...e, status: 'RUNNING' } : e)))
    const started = await api.runWorkflow(id)
    if (!started?.ok) {
      reloadLists() // undo the optimistic RUNNING at once
      reportRunFailure(started?.error || 'Run failed', {
        activateAndRun: async () => { await handleToggle(id, true); await handleRun(id) },
      })
      return
    }
    later(reloadLists, 1500)
  }
  const handleStop = async (executionId) => {
    setExecutions(prev => prev.map(e => (e.id === executionId ? { ...e, status: 'CANCELLED' } : e)))
    await api.cancelWorkflow(executionId)
    later(reloadLists, 500)
  }
  const handleToggle = async (id, active) => {
    await api.setWorkflowActive(id, active)
    await reloadLists()
  }

  return (
    <>
      <div className="page-header">
        <div className="page-header-left">
          <div className="page-title">{t('dashboard.title')}</div>
          <div className="page-subtitle">{t('dashboard.subtitle')}{ver ? ` · v${ver.version.replace(/^v/, '')}` : ''}</div>
        </div>
        <div className="page-header-right">
          <div className="dash-scope" role="group" aria-label={t('dashboard.scope.label')}>
            <button className={`btn btn-sm ${scope === 'profile' ? 'btn-secondary' : 'btn-ghost'}`}
              aria-pressed={scope === 'profile'} onClick={() => setScope('profile')}>
              {t('dashboard.scope.profile')}
            </button>
            <button className={`btn btn-sm ${scope === 'global' ? 'btn-secondary' : 'btn-ghost'}`}
              aria-pressed={scope === 'global'} onClick={() => setScope('global')}>
              {t('dashboard.scope.global')}
            </button>
          </div>
          <button className="btn btn-ghost btn-sm" onClick={handleRefresh} style={{ gap: 5 }}>
            <RefreshCw size={13} style={{ animation: refreshing ? 'spin 0.7s linear infinite' : 'none' }} />
            {t('dashboard.refresh')}
          </button>
          <button className="btn btn-secondary btn-sm" onClick={() => onNavigate('noderunner')} style={{ gap: 5 }}>
            <GitBranch size={13} /> {t('dashboard.workflowEditor')}
          </button>
        </div>
      </div>

      <div className="page-body dash-page">
        {noProfile && scope === 'profile' ? (
          <div className="card dash-empty-state" data-testid="dash-no-profile">
            <div className="section-title">{t('dashboard.noProfile.title')}</div>
            <p>{t('dashboard.noProfile.body')}</p>
            <button className="btn btn-primary btn-sm" onClick={() => setNewProfileOpen(true)}>
              {t('dashboard.noProfile.action')}
            </button>
            <NewProfileModal open={newProfileOpen} onClose={() => setNewProfileOpen(false)}
              onCreated={(p) => p?.id && switchToProfile(p.id)} />
          </div>
        ) : (<>
        {showGetStarted && (
          <GetStartedCard steps={steps} bridge={bridge} onNavigate={onNavigate}
            onSignedIn={() => gate.check?.('manual')}
            onDismiss={() => { saveDismissed(); setGetStartedHidden(true) }} />
        )}
        <AttentionStrip items={items} loading={loading} failed={summaryFailed} unread={unreadSections(summary)}
          onNavigate={onNavigate} onOpenHil={onOpenHil} />
        <StatRow summary={summary} orgs={orgs} loading={loading} onNavigate={onNavigate} />
        <div className="dashboard-grid">
          <div className="dash-col">
            <WorkflowsCard workflows={workflows} executions={executions} schedules={summary?.schedules}
              onRun={handleRun} onStop={handleStop} onToggle={handleToggle} onNavigate={onNavigate}
              currentId={currentId} />
            <RecentRunsCard executions={executions} onNavigate={onNavigate} currentId={currentId} />
            <ActivityCard summary={summary} onNavigate={onNavigate} />
          </div>
          <div className="dash-col">
            {scope === 'global' && <ProfilesCard summary={summary} />}
            <SystemCard summary={summary} bridge={bridge} onNavigate={onNavigate} />
            <OrgsCard orgs={orgs} onNavigate={onNavigate} />
            <AutomationsCard summary={summary} onNavigate={onNavigate} />
            <AccountsCard summary={summary} onNavigate={onNavigate} />
          </div>
        </div>
        </>)}
      </div>
    </>
  )
}

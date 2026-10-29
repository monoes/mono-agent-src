import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Link2, ExternalLink, Download, Bot } from 'lucide-react'
import { api } from '../services/api.js'
import { GetVersion, CheckForUpdate, AppSelfUpdate, GetActiveProfile } from '../wailsjs/go/main/App'
import { getAssistantTools, getAssistantAllowRuns, setAssistantTools, setAssistantAllowRuns } from '../lib/assistantTools.js'
import RefreshButton from '../components/RefreshButton.jsx'
import HealthSection from '../components/settings/HealthSection.jsx'
import BrowserBindingsSection from '../components/settings/BrowserBindingsSection.jsx'
import JevSection from '../components/settings/JevSection.jsx'
import { useJevData } from '../components/settings/useJevData.js'
import ApiSection from '../components/settings/ApiSection.jsx'
import VaultKeyringSection from '../components/settings/VaultKeyringSection.jsx'
import CoderModeSection from '../components/settings/CoderModeSection.jsx'
import CoderOrgSection from '../components/settings/CoderOrgSection.jsx'
import ExtensionSection from '../components/settings/ExtensionSection.jsx'
import BrowserPermissionsSection from '../components/settings/BrowserPermissionsSection.jsx'
import AiTaskBudgetsSection from '../components/settings/AiTaskBudgetsSection.jsx'
import AgentPurposesSection from '../components/settings/AgentPurposesSection.jsx'
import SandboxFallbackSection from '../components/settings/SandboxFallbackSection.jsx'
import ProfileSection from '../components/settings/ProfileSection.jsx'
import ApiKeysSection from '../components/settings/api/ApiKeysSection.jsx'
import { ScopeGroup } from '../components/settings/ScopeGroup.jsx'

const headingStyle = { display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }
const headingText = { fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700, color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: 2 }

// ── VersionRow ──────────────────────────────────────────────────────────────

function VersionRow() {
  const [ver, setVer] = useState(null)
  const [update, setUpdate] = useState(null)

  useEffect(() => { GetVersion().then(setVer).catch(() => {}) }, [])

  function check() {
    setUpdate({ checking: true })
    CheckForUpdate().then(info => {
      if (info.error) setUpdate({ error: info.error })
      else if (info.update_available) setUpdate({ available: true, latest: info.latest_version })
      else { setUpdate({ upToDate: true }); setTimeout(() => setUpdate(null), 3000) }
    }).catch(e => setUpdate({ error: String(e) }))
  }

  function doUpdate() {
    setUpdate(u => ({ ...u, updating: true }))
    AppSelfUpdate().then(r => {
      if (r.success) setUpdate({ done: true, latest: r.new_version })
      else setUpdate({ error: r.error })
    }).catch(e => setUpdate({ error: String(e) }))
  }

  const vText = ver ? `v${ver.version.replace(/^v/, '')}` : '...'

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>
          Version
        </span>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)' }}>
            MonoAgent {vText}
          </span>
          <button
            onClick={check}
            disabled={update?.checking || update?.updating}
            style={{
              background: 'rgba(0,180,216,.15)',
              color: '#00b4d8',
              border: '1px solid rgba(0,180,216,.2)',
              borderRadius: 4,
              padding: '2px 10px',
              cursor: 'pointer',
              fontFamily: 'var(--font-mono)',
              fontSize: 9,
              opacity: (update?.checking || update?.updating) ? 0.5 : 1,
            }}
          >
            {update?.checking ? 'Checking...' : 'Check for updates'}
          </button>
        </div>
      </div>
      {update?.upToDate && (
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#00f5d4', marginTop: 6 }}>
          You're on the latest version.
        </div>
      )}
      {update?.available && !update.updating && !update.done && (
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, marginTop: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{ color: '#fbbf24' }}>{update.latest} is available</span>
          <button onClick={doUpdate} style={{
            background: '#00b4d8', color: '#fff', border: 'none', borderRadius: 4,
            padding: '3px 12px', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10,
          }}>Install Update</button>
        </div>
      )}
      {update?.updating && (
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#00b4d8', marginTop: 6 }}>
          Updating...
        </div>
      )}
      {update?.done && (
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#00f5d4', marginTop: 6 }}>
          Updated to {update.latest} — restart the app to apply.
        </div>
      )}
      {update?.error && (
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--red)', marginTop: 6 }}>
          {update.error}
        </div>
      )}
    </div>
  )
}

// ── ExportRow ───────────────────────────────────────────────────────────────

function ExportRow() {
  const [state, setState] = useState(null) // { exporting } | { done, summary } | { error }

  function doExport() {
    setState({ exporting: true })
    api.exportData().then(r => {
      if (!r || r.cancelled) { setState(null); return }
      setState({ done: true, summary: `Exported ${r.people_count} people and ${r.actions_count} actions to ${r.output_dir}` })
    }).catch(e => setState({ error: String(e) }))
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>
          Data Export
        </span>
        <button
          onClick={doExport}
          disabled={state?.exporting}
          style={{
            display: 'flex', alignItems: 'center', gap: 5,
            background: 'rgba(0,180,216,.15)',
            color: '#00b4d8',
            border: '1px solid rgba(0,180,216,.2)',
            borderRadius: 4,
            padding: '2px 10px',
            cursor: 'pointer',
            fontFamily: 'var(--font-mono)',
            fontSize: 9,
            opacity: state?.exporting ? 0.5 : 1,
          }}
        >
          <Download size={10} />
          {state?.exporting ? 'Exporting...' : 'Export to JSON'}
        </button>
      </div>
      {state?.done && (
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#00f5d4', marginTop: 6, wordBreak: 'break-all' }}>
          {state.summary}
        </div>
      )}
      {state?.error && (
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--red)', marginTop: 6 }}>
          {state.error}
        </div>
      )}
    </div>
  )
}

// ── AssistantToolsSection ────────────────────────────────────────────────────

// GX2 contract: StartChatTurn takes tools + allowRuns flags (both
// default ON — see lib/assistantTools.js). Persisted to localStorage and
// read by the AI chat panels at send time; a visible indicator in the panel
// shows when tools are active. Toggling applies to the next message sent.
function AssistantToolsSection() {
  const [tools, setTools] = useState(() => getAssistantTools())
  const [allowRuns, setAllowRuns] = useState(() => getAssistantAllowRuns())

  const toggleTools = (on) => {
    setAssistantTools(on)
    setTools(on)
    if (!on) setAllowRuns(false)
  }

  const toggleAllowRuns = (on) => {
    setAssistantAllowRuns(on)
    setAllowRuns(on && tools)
  }

  const checkbox = (checked, disabled, onChange) => ({
    type: 'checkbox',
    checked, disabled,
    onChange: e => onChange(e.target.checked),
    style: { marginTop: 2, accentColor: '#00b4d8', flexShrink: 0 },
  })

  return (
    <div style={{
      background: 'var(--surface)',
      border: '1px solid var(--border)',
      borderRadius: 'var(--radius-lg)',
      padding: '16px 20px',
      display: 'flex', flexDirection: 'column', gap: 12,
      marginBottom: 16,
    }}>
      <label style={{ display: 'flex', alignItems: 'flex-start', gap: 8, cursor: 'pointer' }}>
        <input {...checkbox(tools, false, toggleTools)} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)', lineHeight: 1.5 }}>
          Enable monoagent tools (workflows, people, vault metadata)
        </span>
      </label>
      <label style={{ display: 'flex', alignItems: 'flex-start', gap: 8, cursor: tools ? 'pointer' : 'default', opacity: tools ? 1 : 0.45, paddingLeft: 24 }}>
        <input {...checkbox(allowRuns, !tools, toggleAllowRuns)} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)', lineHeight: 1.5 }}>
          Allow running workflows/actions from chat
        </span>
      </label>
      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)', lineHeight: 1.6 }}>
        Both options are on by default. The AI assistant can read your workflows,
        people and vault metadata; the second option additionally lets it trigger
        workflow runs and actions from chat.
      </div>
    </div>
  )
}

// ── QuickAccessCard ─────────────────────────────────────────────────────────

function QuickAccessCard({ icon: Icon, title, description, stats, onClick }) {
  const [hov, setHov] = useState(false)
  return (
    <div
      onClick={onClick}
      onMouseEnter={() => setHov(true)}
      onMouseLeave={() => setHov(false)}
      style={{
        flex: 1,
        background: hov ? 'var(--elevated)' : 'var(--surface)',
        border: hov ? '1px solid var(--border-bright)' : '1px solid var(--border)',
        borderRadius: 'var(--radius-lg)',
        padding: '20px 18px',
        cursor: 'pointer',
        transition: 'all var(--transition)',
        display: 'flex',
        flexDirection: 'column',
        gap: 10,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <div style={{
          width: 34, height: 34, borderRadius: 'var(--radius)',
          background: 'rgba(0,180,216,.1)', border: '1px solid rgba(0,180,216,.15)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
        }}>
          <Icon size={16} style={{ color: '#00b4d8' }} />
        </div>
        <div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 600, color: 'var(--text)' }}>
            {title}
          </div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', marginTop: 2 }}>
            {description}
          </div>
        </div>
      </div>
      {stats && (
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-dim)', paddingLeft: 44 }}>
          {stats}
        </div>
      )}
    </div>
  )
}

// ── Main ─────────────────────────────────────────────────────────────────────

// LanguageSection is a minimal pilot locale switcher — a real Settings UI
// might style this like the other cards, but the goal here is proving the
// i18next changeLanguage() mechanism works end-to-end, not visual polish.
function LanguageSection() {
  const { t, i18n } = useTranslation()
  return (
    <div style={{
      background: 'var(--surface)',
      border: '1px solid var(--border)',
      borderRadius: 'var(--radius-lg)',
      padding: '16px 20px',
      display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 16,
      marginBottom: 28,
    }}>
      <div>
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)', marginBottom: 2 }}>
          {t('settings.language.label')}
        </div>
        <div style={{ fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)' }}>
          {t('settings.language.hint')}
        </div>
      </div>
      <select
        value={i18n.resolvedLanguage || i18n.language}
        onChange={e => i18n.changeLanguage(e.target.value)}
      >
        <option value="en">English</option>
        <option value="es">Español</option>
      </select>
    </div>
  )
}

export default function Settings({ onNavigate, navData, isActive = true }) {
  const { t } = useTranslation()
  // Deep links (the dashboard's "health" and "Jev" rows) scroll to a section.
  const sectionRefs = { health: useRef(null), jev: useRef(null), version: useRef(null) }
  const section = navData?.section
  useEffect(() => {
    const el = section && sectionRefs[section]?.current
    if (el) el.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [navData])
  const [dbPath, setDbPath] = useState('')
  const [dbConnected, setDbConnected] = useState(false)
  const [connCount, setConnCount] = useState(null)
  const [refreshing, setRefreshing] = useState(false)
  const [profile, setProfile] = useState({ id: '', name: '' })
  const [apiStatus, setApiStatus] = useState(null) // the API section's status, shared with the keys block
  const jev = useJevData({ withUsage: true }) // one JevStatus + one JevUsage for both Jev parts

  const load = useCallback(async () => {
    api.getDBPath().then(p => setDbPath(p || ''))
    api.isDBConnected().then(c => setDbConnected(!!c))
    // Count active connections
    return api.listConnections('').then(conns => {
      if (Array.isArray(conns)) {
        const active = conns.filter(c => (c.Status || c.status) === 'active').length
        setConnCount(`${active} active connection${active !== 1 ? 's' : ''}`)
      }
    }).catch(() => {})
  }, [])

  useEffect(() => { load() }, [load])

  // The app keeps this page mounted while another is shown, and the profile can be switched there: the profile is
  // read again each time the page comes back, and with it the Jev status and usage, whose profile half (feature
  // toggles, thresholds, 7-day usage) belongs to the profile that was active when they were read.
  const jevLoad = jev.load
  const readProfile = useCallback((reloadJev) => {
    try {
      GetActiveProfile().then(p => {
        setProfile({ id: p?.id || '', name: p?.name || '', icon: p?.icon || '' })
        if (reloadJev) jevLoad()
      }).catch(() => {})
    } catch { /* no name: the heading says "the active profile" */ }
  }, [jevLoad])
  const wasActive = useRef(isActive)
  useEffect(() => {
    const returned = isActive && !wasActive.current
    wasActive.current = isActive
    if (returned) { readProfile(true); load() }
  }, [isActive, readProfile, load])
  useEffect(() => { readProfile(false) }, [readProfile])

  const handleRefresh = async () => {
    setRefreshing(true)
    try { await load() } finally { setRefreshing(false) }
  }

  return (
    <>
      <div className="page-header">
        <div className="page-header-left">
          <div className="page-title">{t('settings.title')}</div>
          <div className="page-subtitle">{t('settings.subtitle')}</div>
        </div>
        <div className="page-header-right">
          <RefreshButton onClick={handleRefresh} loading={refreshing} />
        </div>
      </div>

      <div className="page-body">
        <ScopeGroup scope="global">
          {/* AI agents (runtimes are installed per machine) */}
          <div style={{ display: 'flex', gap: 12, marginBottom: 28 }}>
            <QuickAccessCard
              icon={Bot}
              title={t('settings.aiAgentsTitle')}
              description={t('settings.aiAgentsDesc')}
              onClick={() => onNavigate?.('ai')}
            />
          </div>

          {/* System health (monoagentcli doctor) */}
          <div ref={sectionRefs.health} data-section="health" style={headingStyle}>
            <span style={headingText}>{t('settings.health.sectionTitle')}</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <HealthSection onNavigate={onNavigate} collapsible defaultExpanded={false} />

          {/* Language */}
          <div style={headingStyle}>
            <span style={headingText}>{t('settings.language.sectionTitle')}</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <LanguageSection />

          {/* Assistant tool access */}
          <div style={headingStyle}>
            <span style={headingText}>Assistant Tool Access</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <AssistantToolsSection />

          {/* Coder mode (#203) */}
          <div style={headingStyle}>
            <span style={headingText}>Coder Mode</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <CoderModeSection />

          {/* Coder chats' dynamic org and agents' sends (epic #475) */}
          <div style={headingStyle}>
            <span style={headingText}>{t('coderOrg.title')}</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <CoderOrgSection part="machine" />

          {/* The browser extension: the only way monoagent drives a browser */}
          <div style={headingStyle}>
            <span style={headingText}>Browser Extension</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <ExtensionSection />

          {/* What AI agents may do in the browsers (machine-wide; plan C8) */}
          <div style={headingStyle}>
            <span style={headingText}>Agent Browser Permissions</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <BrowserPermissionsSection />

          {/* Spend caps for AI tasks handed to the coder */}
          <div style={headingStyle}>
            <span style={headingText}>AI Task Budgets</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <AiTaskBudgetsSection />

          {/* Which validated models agents may use for each kind of work */}
          <div style={headingStyle}>
            <span style={headingText}>{t('settings.purposes.sectionTitle')}</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <AgentPurposesSection />

          {/* Agent sandbox fallback */}
          <div style={headingStyle}>
            <span style={headingText}>{t('settings.sandboxFallback.sectionTitle')}</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <SandboxFallbackSection />

          <div ref={sectionRefs.jev} data-section="jev">
            <JevSection part="machine" jev={jev} collapsible defaultExpanded={section === 'jev'} expandToken={section === 'jev' ? navData : null} />
          </div>

          <ApiSection part="machine" onNavigate={onNavigate} isActive={isActive} onStatus={setApiStatus} />

          {/* Application Info */}
          <div ref={sectionRefs.version} data-section="version" style={headingStyle}>
            <span style={headingText}>Application Info</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <div style={{
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: 'var(--radius-lg)',
            padding: '16px 20px',
            display: 'flex', flexDirection: 'column', gap: 14,
            marginBottom: 16,
          }}>
            {/* DB path */}
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 16 }}>
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, flexShrink: 0, paddingTop: 1 }}>
                Database Path
              </span>
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)', wordBreak: 'break-all', textAlign: 'right' }}>
                {dbPath || '—'}
              </span>
            </div>

            {/* DB status */}
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>
                Database Status
              </span>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <span style={{
                  width: 7, height: 7, borderRadius: '50%',
                  background: dbConnected ? 'var(--green-neon)' : 'var(--red)',
                  boxShadow: dbConnected ? '0 0 5px var(--green-neon)' : 'none',
                }} />
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: dbConnected ? 'var(--green-neon)' : 'var(--red)' }}>
                  {dbConnected ? 'Connected' : 'Disconnected'}
                </span>
              </div>
            </div>

            {/* App version + update */}
            <VersionRow />
          </div>
        </ScopeGroup>

        <ScopeGroup scope="profile" profileName={profile.name}>
          {/* Connections (OAuth credentials and accounts are the profile's) */}
          <div style={{ display: 'flex', gap: 12, marginBottom: 28 }}>
            <QuickAccessCard
              icon={Link2}
              title={t('settings.connectionsTitle')}
              description={t('settings.connectionsDesc')}
              stats={connCount}
              onClick={() => onNavigate?.('connections')}
            />
          </div>

          {/* Profile: name, icon, delete */}
          <div style={headingStyle}>
            <span style={headingText}>{t('settings.profile.sectionTitle')}</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <ProfileSection profile={profile} isActive={isActive} />

          <BrowserBindingsSection />

          <CoderOrgSection part="profile" />

          <JevSection part="profile" jev={jev} />

          <ApiKeysSection isActive={isActive} status={apiStatus} profileId={profile.id} />

          {/* Export people/actions as JSON (runs with --profile) */}
          <div style={headingStyle}>
            <span style={headingText}>Data Export</span>
            <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
          </div>

          <div style={{
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: 'var(--radius-lg)',
            padding: '16px 20px',
            marginBottom: 16,
          }}>
            <ExportRow />
          </div>

          {/* Note */}
          <div style={{
            fontFamily: 'var(--font-body)',
            fontSize: 11,
            color: 'var(--text-muted)',
            lineHeight: 1.6,
            padding: '10px 14px',
            background: 'rgba(0,245,212,.04)',
            border: '1px solid rgba(0,245,212,.12)',
            borderRadius: 'var(--radius)',
          }}>
            All authentication is managed in <strong style={{ color: 'var(--text-secondary)', cursor: 'pointer' }} onClick={() => onNavigate?.('connections')}>Connections</strong>.
            OAuth credentials, API keys, and browser sessions are configured there.
          </div>
        </ScopeGroup>
      </div>
    </>
  )
}

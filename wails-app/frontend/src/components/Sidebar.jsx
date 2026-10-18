import { useState, useEffect, useCallback, useRef } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import {
  LayoutDashboard, Users,
  Terminal, PlayCircle, Settings, Image, Mail, KeyRound,
  ChevronDown, Plus, Check, Building2, FolderOpen, FolderCog, Loader2, Briefcase, FileText, Send
} from 'lucide-react'
import { GetVersion } from '../wailsjs/go/main/App'
import * as WailsApp from '../wailsjs/go/main/App'
import { notify } from '../services/api.js'
import { confirm } from './ConfirmDialog.jsx'
import NewProfileModal from './NewProfileModal.jsx'
import { iconUrl } from './orgdesigner/roleIcons.js'

const GetProfiles          = WailsApp.GetProfiles          ?? (async () => [])
const SwitchProfile        = WailsApp.SwitchProfile        ?? (async () => {})
const ChooseProfileFolder  = WailsApp.ChooseProfileFolder  ?? (async () => '')
const MoveProfileFolder    = WailsApp.MoveProfileFolder    ?? (async () => {})
const RevealProfileFolder  = WailsApp.RevealProfileFolder  ?? (async () => {})

// labelKey indexes sidebar.nav.* in src/locales/<lang>.json — see docs/i18n.md.
const NAV_ITEMS = [
  { id: 'dashboard',   labelKey: 'dashboard',   icon: LayoutDashboard, section: 'MAIN' },
  { id: 'noderunner',  labelKey: 'noderunner',  icon: PlayCircle,      section: 'MAIN' },
  { id: 'orgs',        labelKey: 'orgs',        icon: Building2,       section: 'MAIN' },
  { id: 'people',      labelKey: 'people',      icon: Users,           section: 'DATA' },
  { id: 'publication', labelKey: 'publication', icon: Send, section: 'DATA' },
  { id: 'communications', labelKey: 'communications', icon: Mail,      section: 'DATA' },
  { id: 'vault',       labelKey: 'vault',       icon: Image,           section: 'DATA' },
  { id: 'secretsVault', labelKey: 'secretsVault', icon: KeyRound,      section: 'DATA' },
  { id: 'applications', labelKey: 'applications', icon: Briefcase,     section: 'DATA' },
  { id: 'documents',   labelKey: 'documents',   icon: FileText,        section: 'DATA' },
  { id: 'logs',        labelKey: 'logs',        icon: Terminal,        section: 'DEBUG' },
  { id: 'settings',    labelKey: 'settings',    icon: Settings,        section: 'SYSTEM' },
]

function requestNotifyPermission() {
  if (typeof Notification !== 'undefined' && Notification.permission === 'default') {
    Notification.requestPermission().catch(() => {})
  }
}

export default function Sidebar({ activePage, onNavigate, stats, dbConnected }) {
  const { t } = useTranslation()
  const [ver, setVer] = useState(null)
  const [profiles, setProfiles] = useState([])
  const [activeProfileID, setActiveProfileID] = useState('default')
  const [profileOpen, setProfileOpen] = useState(false)
  const [showNewProfileModal, setShowNewProfileModal] = useState(false)
  const [profileError, setProfileError] = useState('')
  const [movingProfileID, setMovingProfileID] = useState(null)
  const [dropPos, setDropPos] = useState({ top: 0, left: 0, width: 0 })
  const profileDropRef = useRef(null)
  const profileBtnRef = useRef(null)

  useEffect(() => { GetVersion().then(setVer).catch(() => {}) }, [])
  useEffect(() => { requestNotifyPermission() }, [])

  const loadProfiles = useCallback(async () => {
    try {
      const list = await GetProfiles()
      setProfiles(Array.isArray(list) ? list : [])
      const active = (list ?? []).find(p => p.is_active)
      if (active) setActiveProfileID(active.id)
    } catch { /* non-fatal */ }
  }, [])

  useEffect(() => { loadProfiles() }, [loadProfiles])

  // Close dropdown when clicking outside.
  useEffect(() => {
    if (!profileOpen) return
    const handler = (e) => {
      if (
        profileDropRef.current && !profileDropRef.current.contains(e.target) &&
        profileBtnRef.current && !profileBtnRef.current.contains(e.target)
      ) {
        setProfileOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [profileOpen])

  const updateDropPos = () => {
    if (profileBtnRef.current) {
      const rect = profileBtnRef.current.getBoundingClientRect()
      setDropPos({ top: rect.bottom + 4, left: rect.left, width: rect.width })
    }
  }

  const toggleProfileOpen = () => {
    if (!profileOpen) updateDropPos()
    setProfileOpen(v => !v)
    setProfileError('')
  }

  // Reposition dropdown when window resizes while open.
  useEffect(() => {
    if (!profileOpen) return
    window.addEventListener('resize', updateDropPos)
    return () => window.removeEventListener('resize', updateDropPos)
  }, [profileOpen])

  const handleSwitchProfile = async (id) => {
    setProfileError('')
    try {
      await SwitchProfile(id)
      setActiveProfileID(id)
      setProfileOpen(false)
      // Reload the page to re-fetch all profile-scoped data.
      window.location.reload()
    } catch (e) {
      setProfileError(e?.message || 'Failed to switch profile')
    }
  }

  const handleProfileCreated = async (profile) => {
    await loadProfiles()
    if (profile?.id) await handleSwitchProfile(profile.id)
  }

  const handleMoveProfileFolder = async (id) => {
    if (movingProfileID) return
    try {
      const path = await ChooseProfileFolder()
      if (!path) return
      // Moving the profile folder is destructive to anything currently
      // running out of it — confirm before proceeding, and surface the
      // outcome on the app toast bus rather than the dropdown's inline
      // error line (which closes with the dropdown and is easy to miss).
      const ok = await confirm(
        'This moves the entire profile folder — workflows keep running may fail. Continue?',
        { title: 'Move profile folder', confirmLabel: 'Move', danger: true },
      )
      if (!ok) return
      setMovingProfileID(id)
      await MoveProfileFolder(id, path)
      notify('profile move', `Profile folder moved to ${path}`)
      await loadProfiles()
    } catch (e) {
      notify('profile move', e?.message || 'Failed to move profile folder')
    } finally {
      setMovingProfileID(null)
    }
  }

  const handleRevealFolder = async (id) => {
    try {
      await RevealProfileFolder(id)
    } catch (e) {
      setProfileError(e?.message || 'Failed to open folder')
    }
  }

  const activeProfileName = profiles.find(p => p.id === activeProfileID)?.name ?? 'Default'

  const getBadge = (id) => {
    if (!stats) return null
    if (id === 'people' && stats.total_people > 0) return stats.total_people
    return null
  }

  const sections = [...new Set(NAV_ITEMS.map(i => i.section))]

  return (
    <aside className="sidebar">
      <div className="sidebar-titlebar">
        <div className="sidebar-logo">
          <img src="/monkey-logo.png" alt="MonoAgent" style={{ width: 40, height: 40, objectFit: 'contain', background: 'none', flexShrink: 0 }} />
          <div>
            <div className="logo-text">MonoAgent</div>
            <div className="logo-sub">{ver ? `v${ver.version.replace(/^v/, '')}` : ''}</div>
          </div>
        </div>

        {/* Profile switcher */}
        <div style={{ marginTop: 10, marginBottom: 2 }}>
          <button
            ref={profileBtnRef}
            onClick={toggleProfileOpen}
            style={{
              display: 'flex', alignItems: 'center', gap: 6, width: '100%',
              padding: '6px 10px', background: 'rgba(0,180,216,0.07)',
              border: '1px solid rgba(0,180,216,0.15)', borderRadius: 6,
              cursor: 'pointer', color: 'var(--text-secondary)', fontSize: 12,
              fontFamily: 'var(--font-mono)',
            }}
          >
            <span style={{ flex: 1, textAlign: 'left', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {activeProfileName}
            </span>
            <ChevronDown size={11} style={{ flexShrink: 0, transform: profileOpen ? 'rotate(180deg)' : 'none', transition: 'transform 0.15s' }} />
          </button>
        </div>

        {profileOpen && createPortal(
          <div
            ref={profileDropRef}
            style={{
              position: 'fixed', top: dropPos.top, left: dropPos.left, width: dropPos.width,
              zIndex: 9999, background: '#0d1a28',
              border: '1px solid rgba(0,180,216,0.2)', borderRadius: 8,
              overflow: 'hidden', boxShadow: '0 8px 24px rgba(0,0,0,0.5)',
            }}
          >
            {profiles.map(p => (
              <div
                key={p.id}
                onClick={() => p.id !== activeProfileID && handleSwitchProfile(p.id)}
                style={{
                  display: 'flex', alignItems: 'center', gap: 8,
                  padding: '9px 12px', cursor: p.id === activeProfileID ? 'default' : 'pointer',
                  fontSize: 12, color: p.id === activeProfileID ? '#00b4d8' : 'var(--text-secondary)',
                  background: p.id === activeProfileID ? 'rgba(0,180,216,0.07)' : 'transparent',
                }}
                onMouseEnter={e => { if (p.id !== activeProfileID) e.currentTarget.style.background = 'rgba(255,255,255,0.05)' }}
                onMouseLeave={e => { e.currentTarget.style.background = p.id === activeProfileID ? 'rgba(0,180,216,0.07)' : 'transparent' }}
              >
                {p.id === activeProfileID ? <Check size={11} color="#00b4d8" /> : <span style={{ width: 11 }} />}
                {p.icon && (
                  <img
                    src={iconUrl(p.icon)}
                    alt=""
                    style={{ width: 18, height: 18, borderRadius: '50%', flexShrink: 0 }}
                  />
                )}
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{p.name}</div>
                  {p.root_dir && (
                    <div
                      title={p.root_dir}
                      style={{
                        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        fontSize: 10, color: 'var(--text-muted)', marginTop: 1, cursor: 'default',
                      }}
                    >{p.root_dir}</div>
                  )}
                </div>
                <button
                  onClick={e => { e.stopPropagation(); handleRevealFolder(p.id) }}
                  title={t('sidebar.showInFinder')}
                  style={{
                    flexShrink: 0, display: 'flex', alignItems: 'center', justifyContent: 'center',
                    width: 20, height: 20, padding: 0, background: 'transparent', border: 'none',
                    color: 'var(--text-muted)', cursor: 'pointer',
                  }}
                  onMouseEnter={e => { e.currentTarget.style.color = 'var(--text-secondary)' }}
                  onMouseLeave={e => { e.currentTarget.style.color = 'var(--text-muted)' }}
                >
                  <FolderOpen size={12} />
                </button>
                <button
                  onClick={e => { e.stopPropagation(); handleMoveProfileFolder(p.id) }}
                  disabled={movingProfileID === p.id}
                  title={t('sidebar.changeProfileFolder')}
                  style={{
                    flexShrink: 0, display: 'flex', alignItems: 'center', justifyContent: 'center',
                    width: 20, height: 20, padding: 0, background: 'transparent', border: 'none',
                    color: 'var(--text-muted)', cursor: movingProfileID === p.id ? 'default' : 'pointer',
                  }}
                  onMouseEnter={e => { if (movingProfileID !== p.id) e.currentTarget.style.color = 'var(--text-secondary)' }}
                  onMouseLeave={e => { e.currentTarget.style.color = 'var(--text-muted)' }}
                >
                  {movingProfileID === p.id
                    ? <Loader2 size={12} style={{ animation: 'spin 0.7s linear infinite' }} />
                    : <FolderCog size={12} />}
                </button>
              </div>
            ))}

            {profileError && (
              <div style={{ padding: '6px 12px', fontSize: 11, color: '#ff6b6b', borderTop: '1px solid rgba(255,0,0,0.1)' }}>
                {profileError}
              </div>
            )}
            <div style={{ borderTop: '1px solid rgba(255,255,255,0.06)', padding: '8px' }}>
              <button
                onClick={() => { setProfileOpen(false); setShowNewProfileModal(true) }}
                style={{
                  display: 'flex', alignItems: 'center', gap: 6, width: '100%',
                  padding: '6px 4px', background: 'transparent', border: 'none',
                  color: 'var(--text-muted)', fontSize: 12, cursor: 'pointer',
                }}
                onMouseEnter={e => { e.currentTarget.style.color = 'var(--text-secondary)' }}
                onMouseLeave={e => { e.currentTarget.style.color = 'var(--text-muted)' }}
              >
                <Plus size={11} /> New profile
              </button>
            </div>
          </div>,
          document.body
        )}
      </div>

      <NewProfileModal
        open={showNewProfileModal}
        onClose={() => setShowNewProfileModal(false)}
        onCreated={handleProfileCreated}
      />

      <nav className="sidebar-nav" aria-label="Main navigation">
        {sections.map(section => (
          <div key={section}>
            <div className="nav-section-label">{t(`sidebar.section.${section}`)}</div>
            {NAV_ITEMS.filter(i => i.section === section).map(item => {
              const Icon = item.icon
              const badge = getBadge(item.id)
              const isActive = activePage === item.id
              const label = t(`sidebar.nav.${item.labelKey}`)
              return (
                <div
                  key={item.id}
                  className={`nav-item ${isActive ? 'active' : ''}`}
                  onClick={() => onNavigate(item.id)}
                  onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && onNavigate(item.id)}
                  role="button"
                  tabIndex={0}
                  aria-current={isActive ? 'page' : undefined}
                  aria-label={label}
                >
                  <Icon className="nav-icon" size={15} />
                  <span>{label}</span>
                  {badge != null && (
                    <span className="nav-badge" aria-label={`${badge} items`}>{badge > 999 ? '999+' : badge}</span>
                  )}
                </div>
              )
            })}
          </div>
        ))}
      </nav>

      <div className="sidebar-footer">
        <div className="db-status">
          <span className={`status-dot ${dbConnected ? 'connected pulse' : 'disconnected'}`} />
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {dbConnected ? 'DB connected' : 'DB offline'}
          </span>
        </div>
      </div>
    </aside>
  )
}

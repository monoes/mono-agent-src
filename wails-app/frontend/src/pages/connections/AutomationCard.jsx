// One browser automation package as a card (spec §7.1), plus the
// "Create automation" card that explains how new packages are made.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '../../i18n.js'
import { Plus, Circle, FileCode, AlertTriangle, ArrowUpCircle, PenLine, Trash2 } from 'lucide-react'
import { Chip, Dot, SOURCE_LABELS, mono, muted, body } from './ui.jsx'

// dashed: removed built-ins and the Create card. Part of the border
// shorthand — mixing in borderStyle makes React warn on every re-render.
const cardStyle = (active, hov, dashed = false) => ({
  background: active ? 'linear-gradient(145deg,var(--elevated),var(--surface))' : 'var(--surface)',
  border: `1px ${dashed ? 'dashed' : 'solid'} ${active ? 'var(--border-active)' : hov ? 'var(--border-bright)' : 'var(--border)'}`,
  borderRadius: 'var(--radius-lg)',
  padding: '12px 14px',
  cursor: 'pointer',
  display: 'flex', flexDirection: 'column', gap: 7,
  transition: 'all var(--transition)',
  boxShadow: active ? 'var(--shadow-glow-sm)' : 'none',
  minWidth: 0, textAlign: 'left',
})

// loginText reads the CLI's session block; status (active | expired |
// logged_out) is used when the CLI sends it, else only loggedIn.
export function loginText(s) {
  if (s?.loggedIn) return s.username ? i18n.t('ui6ConnAuto.logged_in_user', { name: s.username }) : i18n.t('ui6ConnAuto.logged_in')
  if (s?.status === 'expired') return i18n.t('ui6ConnAuto.session_expired')
  return i18n.t('ui6ConnAuto.logged_out')
}

export default function AutomationCard({ automation: a, onOpen }) {
  const { t } = useTranslation()
  const [hov, setHov] = useState(false)
  const s = a.session || {}
  const removed = !!a.removed
  const unavailable = !removed && a.available === false
  const disabled = !removed && !unavailable && a.enabled === false
  return (
    <button
      type="button"
      onClick={onOpen}
      onMouseEnter={() => setHov(true)}
      onMouseLeave={() => setHov(false)}
      aria-label={removed ? t('ui6ConnAuto.card_uninstalled', { name: a.name || a.id }) : t('ui6ConnAuto.card_label', { name: a.name || a.id, login: loginText(s), count: a.actions || 0 })}
      style={{ ...cardStyle(s.loggedIn && !unavailable && !removed, hov, removed), opacity: removed ? 0.5 : unavailable || disabled ? 0.75 : 1 }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
        <span title={a.name || a.id} style={{ ...mono, fontSize: 12, fontWeight: 700, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1 }}>
          {a.name || a.id}
        </span>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <Dot on={s.loggedIn} warn={!s.loggedIn && s.status === 'expired'} />
        <span title={loginText(s)} style={{ ...muted, fontSize: 10, color: s.loggedIn ? 'var(--green-neon)' : 'var(--text-muted)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {loginText(s)}
        </span>
      </div>
      <span style={{ ...muted, fontSize: 10 }}>{t('ui6ConnAuto.actions_count', { count: a.actions || 0 })}</span>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
        <Chip color={a.source === 'imported' ? 'var(--purple-light)' : a.source === 'local' ? 'var(--teal)' : 'var(--cyan)'}>
          {SOURCE_LABELS[a.source] || a.source} {a.version}
        </Chip>
        {removed && <Chip title={t('ui6ConnAuto.uninstalled_title')}><Trash2 size={8} /> {t('ui6ConnAuto.uninstalled')}</Chip>}
        {unavailable && <Chip color="var(--red)" title={a.unavailableReason}><AlertTriangle size={8} /> {t('ui6ConnAuto.unavailable')}</Chip>}
        {disabled && <Chip><Circle size={7} /> {t('ui6ConnAuto.disabled')}</Chip>}
        {a.modified && <Chip color="var(--yellow)" title={t('ui6ConnAuto.modified_title')}><PenLine size={8} /> {t('ui6ConnAuto.modified')}</Chip>}
        {a.containsScripts && <Chip color="var(--orange)" title={t('ui6ConnAuto.scripts_title')}><FileCode size={8} /> {t('ui6ConnAuto.contains_scripts')}</Chip>}
        {a.pendingUpdate && <Chip color="var(--green-neon)" title={t('ui6ConnAuto.pending_title', { version: a.pendingUpdate })}><ArrowUpCircle size={8} /> {t('ui6ConnAuto.pending_update')}</Chip>}
      </div>
    </button>
  )
}

export function CreateAutomationCard({ onRecord, onImport }) {
  const { t } = useTranslation()
  return (
    <div style={{ ...cardStyle(false, false, true), cursor: 'default', gap: 8 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, ...mono, fontSize: 12, fontWeight: 700, color: 'var(--text)' }}>
        <Plus size={13} /> {t('ui6ConnAuto.create_title')}
      </div>
      <div style={{ ...body, fontSize: 11 }}>
        {t('ui6ConnAuto.create_body')}
      </div>
      <div style={{ display: 'flex', gap: 6, marginTop: 'auto' }}>
        <button className="btn btn-secondary btn-sm" onClick={onRecord}>{t('ui6Conn.record_new')}</button>
        <button className="btn btn-ghost btn-sm" onClick={onImport}>{t('ui6ConnAuto.import')}</button>
      </div>
    </div>
  )
}

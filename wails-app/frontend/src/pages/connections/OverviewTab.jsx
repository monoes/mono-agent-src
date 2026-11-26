// Overview tab: manifest summary, site domains, permissions, trust and
// validation issues for one automation package.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import '../../i18n.js'
import { ExternalLink } from 'lucide-react'
import { api } from '../../services/api.js'
import { confirm } from '../../components/ConfirmDialog.jsx'
import { Chip, KV, ErrorBox, Busy, SOURCE_LABELS, trustName, tierName, body, label, mono, muted, panel, fmtDate } from './ui.jsx'

function List({ title, items, empty, color }) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
      <span style={label}>{title}</span>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
        {items && items.length
          ? items.map(x => <Chip key={x} color={color}>{x}</Chip>)
          : <span style={muted}>{empty ?? t('ui6ConnAuto.none')}</span>}
      </div>
    </div>
  )
}

export function IssueList({ issues }) {
  const { t } = useTranslation()
  if (!issues || !issues.length) return null
  return (
    <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 6 }}>
      <span style={label}>{t('ui6ConnAuto.validation')}</span>
      {issues.map((i, n) => (
        <div key={n} style={{ display: 'flex', gap: 8, alignItems: 'baseline' }}>
          <Chip color={i.severity === 'error' ? 'var(--red)' : 'var(--yellow)'}>{t(`ui6ConnAuto.severity_${i.severity}`, { defaultValue: i.severity })}</Chip>
          <span style={{ ...mono, fontSize: 10.5, color: 'var(--text-secondary)', wordBreak: 'break-word' }}>
            {i.file ? `${i.file}${i.stepId ? `#${i.stepId}` : ''}: ` : ''}{i.message} <span style={{ color: 'var(--text-dim)' }}>({i.code})</span>
          </span>
        </div>
      ))}
    </div>
  )
}

const TRUST_TEXT = {
  builtin: 'ui6ConnAuto.trust_text_builtin',
  local: 'ui6ConnAuto.trust_text_local',
  recorded: 'ui6ConnAuto.trust_text_recorded',
  imported: 'ui6ConnAuto.trust_text_imported',
}

// TrustToggles: per-package opt-ins for imported and recorded packages
// (`automation trust`). Built-in and local packages are always trusted.
function TrustToggles({ info, onChanged }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const restricted = info.trust === 'imported' || info.trust === 'recorded'
  const flip = async (kind, on) => {
    const named = { name: info.name || info.id }
    const q = kind === 'scripts'
      ? (on ? t('ui6ConnAuto.allow_scripts_confirm', named) : t('ui6ConnAuto.stop_scripts_confirm', named))
      : (on ? t('ui6ConnAuto.allow_live_confirm', named) : t('ui6ConnAuto.withdraw_live_confirm', named))
    if (!(await confirm(q))) return
    setBusy(kind); setErr('')
    try {
      const res = await api.setAutomationTrust(info.id, on ? kind : `no-${kind}`)
      if (!res || res.error) setErr(res?.error || t('ui6ConnAuto.trust_change_failed'))
      else await onChanged?.()
    } finally { setBusy('') }
  }
  const Toggle = ({ kind, label: text, on, hint }) => (
    <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', cursor: restricted ? 'pointer' : 'default' }}>
      <input type="checkbox" role="switch" checked={!!on} disabled={!restricted || !!busy} onChange={e => flip(kind, e.target.checked)} style={{ marginTop: 2 }} aria-label={text} />
      <span style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
        <span style={{ ...mono, fontSize: 11, color: 'var(--text)' }}>{text}</span>
        <span style={{ ...muted, fontSize: 10 }}>{hint}</span>
      </span>
    </label>
  )
  return (
    <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <span style={label}>{t('ui6ConnAuto.trust')}</span>
        <Chip color={restricted ? 'var(--yellow)' : 'var(--green-neon)'}>{trustName(info.trust || 'imported')}</Chip>
        <span style={{ ...muted, fontSize: 10 }}>{TRUST_TEXT[info.trust] ? t(TRUST_TEXT[info.trust]) : ''}</span>
      </div>
      <Toggle kind="scripts" label={t('ui6ConnAuto.allow_scripts')} on={info.scriptsAllowed} hint={restricted ? t('ui6ConnAuto.allow_scripts_hint') : t('ui6ConnAuto.always_allowed')} />
      <Toggle kind="live" label={t('ui6ConnAuto.allow_live')} on={info.liveRunConfirmed} hint={restricted ? t('ui6ConnAuto.allow_live_hint') : t('ui6ConnAuto.always_allowed')} />
      {busy && <Busy text={t('ui6ConnAuto.saving')} />}
      <ErrorBox>{err}</ErrorBox>
    </div>
  )
}

export default function OverviewTab({ info, manifest, issues, fragments, onTrustChanged }) {
  const { t } = useTranslation()
  const perms = manifest.permissions || {}
  const site = manifest.site || {}
  const publisher = manifest.publisher
  return (
    <>
      {info.unavailableReason && <ErrorBox>{t('ui6ConnAuto.unavailable_reason', { reason: info.unavailableReason })}</ErrorBox>}
      {info.pendingUpdate && (
        <div style={{ ...panel, ...body, fontSize: 11 }}>
          {t('ui6ConnAuto.pending_note', { version: info.pendingUpdate })}
        </div>
      )}
      {manifest.description && <div style={body}>{manifest.description}</div>}
      <TrustToggles info={info} onChanged={onTrustChanged} />
      <KV rows={[
        [t('ui6ConnAuto.source'), SOURCE_LABELS[info.source] || info.source],
        [t('ui6ConnAuto.trust'), trustName(info.trust)],
        [t('ui6ConnAuto.version'), info.version + (info.previousVersion ? ` ${t('ui6ConnAuto.previous_version', { version: info.previousVersion })}` : '')],
        [t('ui6ConnAuto.publisher'), publisher ? publisher.name : '—'],
        manifest.license && [t('ui6ConnAuto.license'), manifest.license],
        [t('ui6ConnAuto.category'), manifest.category || info.category || '—'],
        [t('ui6ConnAuto.policy_tier'), tierName(manifest.policy?.tier || info.tier || 'standard')],
        manifest.requires?.native && [t('ui6ConnAuto.native_bot'), manifest.requires.native],
        [t('ui6ConnAuto.installed_on'), fmtDate(info.installedAt)],
        [t('ui6ConnAuto.location'), info.dir],
      ]} />
      {site.startUrl && (
        <button className="btn btn-ghost btn-sm" onClick={() => api.openURL(site.startUrl)} style={{ alignSelf: 'flex-start', gap: 5 }}>
          <ExternalLink size={11} /> {site.startUrl}
        </button>
      )}
      <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 12 }}>
        <List title={t('ui6ConnAuto.domains_may_visit')} items={site.domains || info.domains} color="var(--cyan)" empty={t('ui6ConnAuto.unrestricted')} />
        <List title={t('ui6ConnAuto.step_types_allowed')} items={perms.steps} empty={t('ui6ConnAuto.all_builtin')} />
        <List title={t('ui6ConnAuto.page_scripts')} items={perms.scripts} color="var(--orange)" />
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <span style={label}>{t('ui6ConnAuto.downloads')}</span>
          <span style={{ ...mono, fontSize: 11, color: perms.downloads ? 'var(--yellow)' : 'var(--text-muted)' }}>{perms.downloads ? t('ui6ConnAuto.allowed') : t('ui6ConnAuto.not_allowed')}</span>
        </div>
        {fragments.length > 0 && <List title={t('ui6ConnAuto.fragments')} items={fragments} />}
      </div>
      <IssueList issues={issues} />
    </>
  )
}

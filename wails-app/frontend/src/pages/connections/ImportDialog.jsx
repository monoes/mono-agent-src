// Import an automation package (spec §6.3): pick a .mpkg (or paste a path /
// URL), review what `automation install --dry-run` reports, then confirm.
// The install passes the reviewed sha256 (--expect-sha256) so the bytes
// installed are the bytes reviewed, and replacing a built-in needs an
// explicit tick (--replace-builtin).
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { X, FolderOpen, AlertTriangle } from 'lucide-react'
import { api } from '../../services/api.js'
import { ErrorBox, OkBox, Busy, body, mono, useDialog } from './ui.jsx'
import ImportReview, { needsReplaceConfirm, replaceTarget, sourceLabel } from './ImportReview.jsx'

export default function ImportDialog({ installedPackages = [], onClose, onInstalled }) {
  const { t } = useTranslation()
  const dialog = useDialog(onClose)
  const [path, setPath] = useState('')
  const [phase, setPhase] = useState('pick') // pick | reviewing | review | installing | done
  const [res, setRes] = useState(null)
  const [error, setError] = useState('')
  const [installed, setInstalled] = useState(null)
  const [replaceOk, setReplaceOk] = useState(false)

  const review = async (p) => {
    const src = (p ?? path).trim()
    if (!src) return
    setPhase('reviewing'); setError(''); setRes(null); setReplaceOk(false)
    const out = await api.installAutomationDryRun(src)
    if (!out || out.error) { setError(out?.error || t('ui6ConnAuto.read_package_failed')); setPhase('pick'); return }
    setRes(out); setPhase('review')
  }
  const browse = async () => {
    const p = await api.chooseAutomationPackage()
    if (!p) return
    setPath(p)
    review(p)
  }
  const install = async () => {
    setPhase('installing'); setError('')
    const out = await api.installAutomation(path.trim(), { expectSha256: res?.sha256 || '', replace: needsReplaceConfirm(res?.review) && replaceOk })
    if (!out || out.error) { setError(out?.error || t('ui6ConnAuto.install_failed')); setPhase('review'); return }
    setInstalled(out); setPhase('done')
    onInstalled?.()
  }

  const insecureURL = /^http:\/\//i.test(path.trim())
  const replaces = needsReplaceConfirm(res?.review) ? replaceTarget(res) : null
  // Opt-ins reset when package content changes (by design): warn on an
  // update when the installed package currently has either allowed.
  const current = installedPackages.find(a => a.id === res?.id && !a.removed)
  const resetsTrust = !!res?.previousVersion && !replaces && !!current && (current.scriptsAllowed || current.liveRunConfirmed) && ['imported', 'recorded'].includes(current.trust)
  const hasErrors = (res?.issues || []).some(i => i.severity === 'error')
  const blocked = phase !== 'review' || hasErrors || (replaces && !replaceOk)
  return (
    <div className="modal-overlay" onClick={e => e.target === e.currentTarget && onClose()} style={{ zIndex: 1100 }}>
      <div {...dialog} className="modal" role="dialog" aria-modal="true" aria-labelledby="import-title" style={{ width: 640 }}>
        <div className="modal-title">
          <span id="import-title">{t('ui6Conn.import_automation')}</span>
          <button className="btn btn-ghost btn-icon" onClick={onClose} aria-label={t('ui6Conn.close')}><X size={15} /></button>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={body}>{t('ui6ConnAuto.choose_before')} <code>.mpkg</code> {t('ui6ConnAuto.choose_after')}</div>
          <div style={{ display: 'flex', gap: 6 }}>
            <input className="form-input" aria-label={t('ui6ConnAuto.path_label')} placeholder={t('ui6ConnAuto.path_placeholder')} value={path}
              onChange={e => { setPath(e.target.value); if (phase !== 'pick') { setPhase('pick'); setRes(null) } }}
              onKeyDown={e => { if (e.key === 'Enter') review() }}
              style={{ ...mono, fontSize: 11, padding: '6px 10px' }} />
            <button className="btn btn-secondary btn-sm" onClick={browse} style={{ gap: 5 }}><FolderOpen size={11} /> {t('ui6ConnAuto.browse')}</button>
            {phase === 'pick' && <button className="btn btn-ghost btn-sm" onClick={() => review()} disabled={!path.trim()}>{t('ui6ConnAuto.review')}</button>}
          </div>
          {insecureURL && (
            <ErrorBox><AlertTriangle size={11} style={{ verticalAlign: -1 }} /> {t('ui6ConnAuto.insecure_url')}</ErrorBox>
          )}
          {phase === 'reviewing' && <Busy text={t('ui6ConnAuto.reading_package')} />}
          <ErrorBox>{error}</ErrorBox>
          {res && phase !== 'done' && <ImportReview res={res} hideCliConfirmHint={!!replaces} resetsTrust={resetsTrust} />}
          {replaces && phase !== 'done' && (
            <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', ...body, fontSize: 11.5, color: 'var(--text)' }}>
              <input type="checkbox" checked={replaceOk} onChange={e => setReplaceOk(e.target.checked)} style={{ marginTop: 2 }} />
              {t('ui6ConnAuto.understand_replace', { source: sourceLabel(replaces.source), id: replaces.id })}
            </label>
          )}
          {phase === 'done' && (
            <OkBox>{installed?.review?.policyBlocked
              ? t('ui6ConnAuto.installed_msg_blocked', { name: installed?.name || installed?.id, version: installed?.version })
              : t('ui6ConnAuto.installed_msg', { name: installed?.name || installed?.id, version: installed?.version })}</OkBox>
          )}
        </div>
        <div className="modal-actions">
          {phase === 'done' ? (
            <button className="btn btn-primary btn-sm" onClick={onClose}>{t('ui6Conn.done')}</button>
          ) : (
            <>
              <button className="btn btn-ghost btn-sm" onClick={onClose}>{t('ui6Conn.cancel')}</button>
              <button className="btn btn-primary btn-sm" onClick={install} disabled={blocked}
                title={hasErrors ? t('ui6ConnAuto.has_errors') : replaces && !replaceOk ? t('ui6ConnAuto.tick_to_confirm') : ''}>
                {phase === 'installing' ? t('ui6ConnAuto.installing') : res?.review?.policyBlocked ? t('ui6ConnAuto.install_disabled') : replaces ? t('ui6ConnAuto.replace_button', { source: sourceLabel(replaces.source), id: replaces.id }) : res?.previousVersion ? t('ui6ConnAuto.update_to', { version: res.version }) : t('ui6ConnAuto.install')}
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

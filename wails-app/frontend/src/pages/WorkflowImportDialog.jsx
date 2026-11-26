// Import a workflow (`monoagentcli workflow import --json`, via the
// ImportWorkflowFull binding). Everything shown is what the CLI reported:
// created / updated / unchanged, the bundled automations with their status,
// the missing ones and the per-package review. Installing bundled
// automations re-runs the same import with --yes (imports are idempotent),
// only after an explicit confirmation.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { X, FolderOpen, ExternalLink, Download, RefreshCw } from 'lucide-react'
import { api } from '../services/api.js'
import { emitAutomationsChanged } from '../lib/appEvents.js'
import { confirm } from '../components/ConfirmDialog.jsx'
import { ErrorBox, OkBox, Busy, body, label, mono, muted, panel, useDialog, trustName } from './connections/ui.jsx'
import { STATUS_TEXT, installSummary, ReviewDetail, COPY_WARNING, copyOfExisting, copyReasonOf, splitBundle, NotIncluded, Automations, Differs, ChangeList } from './workflowImport/bundleParts.jsx'

export { copyOfExisting, copyReasonOf, splitBundle }

export default function WorkflowImportDialog({ onClose, onOpen, onImported, onAutomationsInstalled }) {
  const { t } = useTranslation()
  const dialog = useDialog(onClose)
  const [path, setPath] = useState('')
  const [pasted, setPasted] = useState('')
  const [asNew, setAsNew] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [res, setRes] = useState(null)
  const [installRes, setInstallRes] = useState(null)
  const [replaceOk, setReplaceOk] = useState(false)
  const [replaced, setReplaced] = useState(null) // result of "Replace the existing workflow instead"

  const input = pasted.trim() || path.trim()

  const run = async (opts) => {
    setError('')
    let out
    try { out = await api.importWorkflowFull(input, opts) } catch (e) { setError(String(e?.message || e)); return null }
    if (!out || out.error) { setError(out?.error || t('ui6ConnWf.import_failed')); return null }
    return out
  }
  const doImport = async () => {
    setBusy('import'); setRes(null); setInstallRes(null); setReplaced(null)
    try { const out = await run({ asNew }); if (out) { setRes(out); onImported?.(out) } } finally { setBusy('') }
  }
  const browse = async () => {
    const p = await api.chooseWorkflowFile()
    if (p) { setPath(p); setPasted(''); setRes(null); setInstallRes(null); setError('') }
  }
  const shown = installRes || res
  const { listed, installable: missing, notIncluded, differs, replaceable } = splitBundle(shown?.automations)
  const existingId = !replaced ? copyOfExisting(res) : null
  const otherWarnings = (shown?.warnings || []).filter(w => !COPY_WARNING.test(w))
  const replaceExisting = async () => {
    if (!(await confirm(t('ui6ConnWf.replace_confirm', { name: res.name }), { title: t('ui6ConnWf.replace_title'), confirmLabel: t('ui6ConnWf.replace') }))) return
    setBusy('replace')
    try {
      const out = await run({ replace: existingId, removeCopy: res.id })
      if (out) { setReplaced(out); onImported?.(out) }
    } finally { setBusy('') }
  }
  const installMissing = async () => {
    const message = (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <span>{t('ui6ConnWf.install_confirm', { count: missing.length })}</span>
        {missing.map(i => <ReviewDetail key={i.id} item={i} />)}
        <span>{t('ui6ConnWf.from_file')}</span>
      </div>
    )
    if (!(await confirm(message, { title: t('ui6ConnWf.install_bundled'), confirmLabel: t('ui6ConnWf.install') }))) return
    setBusy('install')
    try {
      // Re-import with --yes: the workflow itself comes back unchanged.
      const out = await run({ yes: true })
      if (out) {
        setInstallRes(out)
        if ((out.automations || []).some(i => i.status === 'installed')) {
          onAutomationsInstalled?.(out)
          emitAutomationsChanged({ source: 'workflow-import' })
        }
      }
    } finally { setBusy('') }
  }

  // Replace installed packages whose bundled copy differs — never
  // automatic: the user confirms the listed changes, then the import is
  // re-run with --replace-automations --yes.
  const replaceDiffering = async () => {
    const message = (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <span>{t('ui6ConnWf.replace_copies', { count: replaceable.length })}</span>
        {replaceable.map(i => (
          <div key={i.id} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11 }}>{i.id} {i.version}</span>
            <ChangeList changes={i.changes} />
          </div>
        ))}
        <span>{t('ui6ConnWf.will_run_file', { count: replaceable.length })}</span>
      </div>
    )
    if (!(await confirm(message, { title: t('ui6ConnWf.replace_with_file'), confirmLabel: t('ui6ConnWf.replace') }))) return
    setBusy('replace-automations')
    try {
      const out = await run({ yes: true, replaceAutomations: true })
      if (out) {
        setInstallRes(out)
        if ((out.automations || []).some(i => i.status === 'replaced' || i.status === 'installed')) {
          onAutomationsInstalled?.(out)
          emitAutomationsChanged({ source: 'workflow-import' })
        }
      }
    } finally { setBusy('') }
  }

  const stillMissing = missing.length > 0
  // A bundled package whose install replaces something needs the same
  // explicit tick as the automation import dialog.
  const replacing = missing.filter(i => i.reviewDetail?.replaceRequired)
  const edit = (fn) => (e) => { fn(e); setRes(null); setInstallRes(null); setError('') }
  return (
    <div className="modal-overlay" onClick={e => e.target === e.currentTarget && onClose()} style={{ zIndex: 1100 }}>
      <div {...dialog} className="modal" role="dialog" aria-modal="true" aria-labelledby="wf-import-title" style={{ width: 600 }}>
        <div className="modal-title">
          <span id="wf-import-title">{t('ui6ConnWf.title')}</span>
          <button className="btn btn-ghost btn-icon" onClick={onClose} aria-label={t('ui6ConnWf.close')}><X size={15} /></button>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={body}>{t('ui6ConnWf.choose_before')} <code>.json</code> {t('ui6ConnWf.choose_after')}</div>
          <div style={{ display: 'flex', gap: 6 }}>
            <input className="form-input" aria-label={t('ui6ConnWf.path_label')} placeholder="/path/to/workflow.json" value={path}
              onChange={edit(e => setPath(e.target.value))} onKeyDown={e => { if (e.key === 'Enter' && input && !busy) doImport() }}
              style={{ ...mono, fontSize: 11, padding: '6px 10px' }} />
            <button className="btn btn-secondary btn-sm" onClick={browse} style={{ gap: 5 }}><FolderOpen size={11} /> {t('ui6ConnWf.browse')}</button>
          </div>
          <textarea className="form-textarea" aria-label={t('ui6ConnWf.paste_label')} placeholder={t('ui6ConnWf.paste_placeholder')} value={pasted}
            onChange={edit(e => setPasted(e.target.value))} rows={4} style={{ fontSize: 11 }} />
          <label style={{ display: 'flex', gap: 8, alignItems: 'center', ...body, fontSize: 11.5 }}>
            <input type="checkbox" checked={asNew} onChange={edit(e => setAsNew(e.target.checked))} />
            {t('ui6ConnWf.as_new')}
          </label>
          {busy === 'import' && <Busy text={t('ui6ConnWf.importing')} />}
          <ErrorBox>{error}</ErrorBox>

          {shown && (
            <>
              <OkBox>
                {replaced
                  ? (replaced.removedCopy ? t('ui6ConnWf.replaced_removed', { name: replaced.name }) : t('ui6ConnWf.replaced_msg', { name: replaced.name }))
                  : installRes
                    ? installSummary(installRes)
                    : (STATUS_TEXT[shown.status] || STATUS_TEXT.created)(shown.name || shown.id)}
              </OkBox>
              {replaced?.removeCopyError && <ErrorBox>{t('ui6ConnWf.copy_not_removed', { error: replaced.removeCopyError })}</ErrorBox>}
              {existingId && (
                <div role="note" style={{ ...panel, borderColor: 'var(--yellow)', display: 'flex', flexDirection: 'column', gap: 8 }}>
                  <span style={{ ...body, fontSize: 11.5 }}>
                    {copyReasonOf(res) === 'edited'
                      ? t('ui6ConnWf.copy_edited', { name: res.name })
                      : t('ui6ConnWf.copy_name', { name: res.name })}
                  </span>
                  <button className="btn btn-secondary btn-sm" onClick={replaceExisting} disabled={!!busy} style={{ alignSelf: 'flex-start' }}>
                    {busy === 'replace' ? t('ui6ConnWf.replacing') : t('ui6ConnWf.replace_instead')}
                  </button>
                </div>
              )}
              {otherWarnings.map((w, i) => <div key={i} role="note" style={{ ...muted, color: 'var(--yellow)' }}>⚠ {w}</div>)}
              <Automations items={listed} />
              <NotIncluded items={notIncluded} />
              <Differs items={differs} />
              {replaceable.length > 0 && (
                <button className="btn btn-secondary btn-sm" onClick={replaceDiffering} disabled={!!busy} style={{ alignSelf: 'flex-start', gap: 5 }}>
                  <RefreshCw size={11} /> {busy === 'replace-automations' ? t('ui6ConnWf.replacing') : t('ui6ConnWf.replace_with_file')}
                </button>
              )}
              {(shown.reviewLines || []).length > 0 && (
                <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 4 }}>
                  <span style={label}>{t('ui6ConnWf.install_review')}</span>
                  {shown.reviewLines.map((l, i) => <span key={i} style={{ ...muted, fontSize: 10.5, wordBreak: 'break-word' }}>{l}</span>)}
                </div>
              )}
              {stillMissing && (
                <div style={{ ...panel, borderColor: 'var(--yellow)', display: 'flex', flexDirection: 'column', gap: 8 }}>
                  <span style={{ ...body, fontSize: 11.5 }}>
                    {t('ui6ConnWf.needs_missing', { count: missing.length, ids: missing.map(i => i.id).join(', ') })}
                  </span>
                  {shown.installCommand && <code style={{ ...mono, fontSize: 10, color: 'var(--text-muted)', wordBreak: 'break-all' }}>{shown.installCommand}</code>}
                  {replacing.length > 0 && (
                    <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', ...body, fontSize: 11.5, color: 'var(--text)' }}>
                      <input type="checkbox" checked={replaceOk} onChange={e => setReplaceOk(e.target.checked)} style={{ marginTop: 2 }} />
                      {t('ui6ConnWf.understand_replace', { items: replacing.map(i => t('ui6ConnWf.the_item', { source: trustName(i.reviewDetail.replaces?.source) || t('ui6ConnWf.installed_source'), id: i.id })).join(', ') })}
                    </label>
                  )}
                  <button className="btn btn-primary btn-sm" onClick={installMissing} disabled={!!busy || (replacing.length > 0 && !replaceOk)} style={{ alignSelf: 'flex-start', gap: 5 }}>
                    <Download size={11} /> {busy === 'install' ? t('ui6ConnWf.installing') : t('ui6ConnWf.install_bundled')}
                  </button>
                </div>
              )}
            </>
          )}
        </div>
        <div className="modal-actions">
          <button className="btn btn-ghost btn-sm" onClick={onClose}>{shown ? t('ui6ConnWf.close') : t('ui6ConnWf.cancel')}</button>
          {shown ? (
            <button className="btn btn-primary btn-sm" onClick={() => onOpen((replaced || shown).id)} style={{ gap: 5 }}><ExternalLink size={11} /> {t('ui6ConnWf.open')}</button>
          ) : (
            <button className="btn btn-primary btn-sm" onClick={doImport} disabled={!input || !!busy}>{t('ui6ConnWf.import')}</button>
          )}
        </div>
      </div>
    </div>
  )
}

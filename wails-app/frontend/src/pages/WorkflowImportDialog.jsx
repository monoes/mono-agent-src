// Import a workflow (`monoagentcli workflow import --json`, via the
// ImportWorkflowFull binding). Everything shown is what the CLI reported:
// created / updated / unchanged, the bundled automations with their status,
// the missing ones and the per-package review. Installing bundled
// automations re-runs the same import with --yes (imports are idempotent),
// only after an explicit confirmation.
import { useState } from 'react'
import { X, FolderOpen, ExternalLink, Download, RefreshCw } from 'lucide-react'
import { api } from '../services/api.js'
import { emitAutomationsChanged } from '../lib/appEvents.js'
import { confirm } from '../components/ConfirmDialog.jsx'
import { ErrorBox, OkBox, Busy, body, label, mono, muted, panel, useDialog } from './connections/ui.jsx'
import { STATUS_TEXT, installSummary, ReviewDetail, COPY_WARNING, copyOfExisting, copyReasonOf, splitBundle, NotIncluded, Automations, Differs, ChangeList } from './workflowImport/bundleParts.jsx'

export { copyOfExisting, copyReasonOf, splitBundle }

export default function WorkflowImportDialog({ onClose, onOpen, onImported, onAutomationsInstalled }) {
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
    if (!out || out.error) { setError(out?.error || 'Import failed.'); return null }
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
    if (!(await confirm(`Replace the existing “${res.name}” with this file? Its current nodes and settings are overwritten, and the copy just imported is removed.`, { title: 'Replace existing workflow', confirmLabel: 'Replace' }))) return
    setBusy('replace')
    try {
      const out = await run({ replace: existingId, removeCopy: res.id })
      if (out) { setReplaced(out); onImported?.(out) }
    } finally { setBusy('') }
  }
  const installMissing = async () => {
    const message = (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <span>Install {missing.length} bundled automation{missing.length === 1 ? '' : 's'} from this workflow file?</span>
        {missing.map(i => <ReviewDetail key={i.id} item={i} />)}
        <span>They come from the file, not from a source you chose separately. Each package is checked against its sha256 and reviewed before anything is written.</span>
      </div>
    )
    if (!(await confirm(message, { title: 'Install bundled automations', confirmLabel: 'Install' }))) return
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
        <span>Replace {replaceable.length === 1 ? 'the installed copy' : `${replaceable.length} installed copies`} with the file's version? What changes:</span>
        {replaceable.map(i => (
          <div key={i.id} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11 }}>{i.id} {i.version}</span>
            <ChangeList changes={i.changes} />
          </div>
        ))}
        <span>Workflows that use {replaceable.length === 1 ? 'it' : 'them'} will run the file's version from now on.</span>
      </div>
    )
    if (!(await confirm(message, { title: "Replace with the file's version", confirmLabel: 'Replace' }))) return
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
          <span id="wf-import-title">Import workflow</span>
          <button className="btn btn-ghost btn-icon" onClick={onClose} aria-label="Close"><X size={15} /></button>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={body}>Choose a workflow <code>.json</code> file, or paste the workflow JSON. Importing the same file again updates it instead of making a copy.</div>
          <div style={{ display: 'flex', gap: 6 }}>
            <input className="form-input" aria-label="Workflow file path" placeholder="/path/to/workflow.json" value={path}
              onChange={edit(e => setPath(e.target.value))} onKeyDown={e => { if (e.key === 'Enter' && input && !busy) doImport() }}
              style={{ ...mono, fontSize: 11, padding: '6px 10px' }} />
            <button className="btn btn-secondary btn-sm" onClick={browse} style={{ gap: 5 }}><FolderOpen size={11} /> Browse</button>
          </div>
          <textarea className="form-textarea" aria-label="Or paste workflow JSON" placeholder='…or paste workflow JSON here: {"name": …, "nodes": […]}' value={pasted}
            onChange={edit(e => setPasted(e.target.value))} rows={4} style={{ fontSize: 11 }} />
          <label style={{ display: 'flex', gap: 8, alignItems: 'center', ...body, fontSize: 11.5 }}>
            <input type="checkbox" checked={asNew} onChange={edit(e => setAsNew(e.target.checked))} />
            Import as a new copy (even if this workflow was imported before)
          </label>
          {busy === 'import' && <Busy text="Importing…" />}
          <ErrorBox>{error}</ErrorBox>

          {shown && (
            <>
              <OkBox>
                {replaced
                  ? `Replaced the existing “${replaced.name}” with this file${replaced.removedCopy ? ' and removed the copy.' : '.'}`
                  : installRes
                    ? installSummary(installRes)
                    : (STATUS_TEXT[shown.status] || STATUS_TEXT.created)(shown.name || shown.id)}
              </OkBox>
              {replaced?.removeCopyError && <ErrorBox>The copy could not be removed: {replaced.removeCopyError}</ErrorBox>}
              {existingId && (
                <div role="note" style={{ ...panel, borderColor: 'var(--yellow)', display: 'flex', flexDirection: 'column', gap: 8 }}>
                  <span style={{ ...body, fontSize: 11.5 }}>
                    {copyReasonOf(res) === 'edited'
                      ? <>“{res.name}” was changed here since it was imported; your edited version was kept, so this file was imported as a separate copy.</>
                      : <>A workflow named “{res.name}” already exists and was left as it is, so this file was imported as a separate copy.</>}
                  </span>
                  <button className="btn btn-secondary btn-sm" onClick={replaceExisting} disabled={!!busy} style={{ alignSelf: 'flex-start' }}>
                    {busy === 'replace' ? 'Replacing…' : 'Replace the existing workflow instead'}
                  </button>
                </div>
              )}
              {otherWarnings.map((w, i) => <div key={i} role="note" style={{ ...muted, color: 'var(--yellow)' }}>⚠ {w}</div>)}
              <Automations items={listed} />
              <NotIncluded items={notIncluded} />
              <Differs items={differs} />
              {replaceable.length > 0 && (
                <button className="btn btn-secondary btn-sm" onClick={replaceDiffering} disabled={!!busy} style={{ alignSelf: 'flex-start', gap: 5 }}>
                  <RefreshCw size={11} /> {busy === 'replace-automations' ? 'Replacing…' : "Replace with the file's version"}
                </button>
              )}
              {(shown.reviewLines || []).length > 0 && (
                <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 4 }}>
                  <span style={label}>Install review</span>
                  {shown.reviewLines.map((l, i) => <span key={i} style={{ ...muted, fontSize: 10.5, wordBreak: 'break-word' }}>{l}</span>)}
                </div>
              )}
              {stillMissing && (
                <div style={{ ...panel, borderColor: 'var(--yellow)', display: 'flex', flexDirection: 'column', gap: 8 }}>
                  <span style={{ ...body, fontSize: 11.5 }}>
                    The workflow was imported, but it needs {missing.length === 1 ? 'an automation that is' : `${missing.length} automations that are`} not installed. Its nodes for {missing.map(i => i.id).join(', ')} will not run until you install them.
                  </span>
                  {shown.installCommand && <code style={{ ...mono, fontSize: 10, color: 'var(--text-muted)', wordBreak: 'break-all' }}>{shown.installCommand}</code>}
                  {replacing.length > 0 && (
                    <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', ...body, fontSize: 11.5, color: 'var(--text)' }}>
                      <input type="checkbox" checked={replaceOk} onChange={e => setReplaceOk(e.target.checked)} style={{ marginTop: 2 }} />
                      I understand this replaces {replacing.map(i => `the ${i.reviewDetail.replaces?.source || 'installed'} ${i.id}`).join(', ')}
                    </label>
                  )}
                  <button className="btn btn-primary btn-sm" onClick={installMissing} disabled={!!busy || (replacing.length > 0 && !replaceOk)} style={{ alignSelf: 'flex-start', gap: 5 }}>
                    <Download size={11} /> {busy === 'install' ? 'Installing…' : 'Install bundled automations'}
                  </button>
                </div>
              )}
            </>
          )}
        </div>
        <div className="modal-actions">
          <button className="btn btn-ghost btn-sm" onClick={onClose}>{shown ? 'Close' : 'Cancel'}</button>
          {shown ? (
            <button className="btn btn-primary btn-sm" onClick={() => onOpen((replaced || shown).id)} style={{ gap: 5 }}><ExternalLink size={11} /> Open</button>
          ) : (
            <button className="btn btn-primary btn-sm" onClick={doImport} disabled={!input || !!busy}>Import</button>
          )}
        </div>
      </div>
    </div>
  )
}

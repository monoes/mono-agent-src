import { useState, useEffect, useRef } from 'react'
import { List, X, Trash2, Library } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import * as WailsApp from '../wailsjs/go/main/App'
import { notify } from '../services/api.js'
import { ExecStatusDot } from './dashboard/RecentRunsCard.jsx'
import { formFromJSON, jsonFromForm, parseTriggerInput } from './triggerInput.js'
import { templateBlurb } from './templateText.js'
import LibraryModal from '../components/library/LibraryModal.jsx'
import LogInToMonoesButton from '../components/library/LogInToMonoesButton.jsx'

// Wails bindings used by the workflow modals, with dev fallbacks.
const ListWorkflows = WailsApp.ListWorkflows ?? (async () => [])
const ListWorkflowTemplates = WailsApp.ListWorkflowTemplates ?? (async () => [])
const CreateWorkflowFromTemplate = WailsApp.CreateWorkflowFromTemplate ?? (async () => { throw new Error('templates unavailable') })
const GetWorkflowExecutions = WailsApp.GetWorkflowExecutions ?? (async () => [])

// Extracted from NodeRunner.jsx to keep that file focused on the canvas editor.
export function SaveModal({ initialName, onConfirm, onClose }) {
  const { t } = useTranslation()
  const [name, setName] = useState(initialName || '')
  const inputRef = useRef(null)
  useEffect(() => { inputRef.current?.focus(); inputRef.current?.select() }, [])

  const submit = () => { if (name.trim()) onConfirm(name.trim()) }

  return (
    <div style={{
      position: 'absolute', inset: 0, zIndex: 200,
      background: 'rgba(2,5,9,0.8)',
      display: 'flex', alignItems: 'center', justifyContent: 'center',
    }} onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div role="dialog" aria-modal="true" aria-label={t('nodeRunner.modals.saveWorkflow')} style={{
        width: 360,
        background: '#080d16',
        border: '1px solid rgba(0,180,216,0.25)',
        borderRadius: 12,
        padding: '20px 20px 16px',
        boxShadow: '0 24px 60px rgba(0,0,0,.85)',
        display: 'flex', flexDirection: 'column', gap: 14,
      }}>
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 700, color: '#e2e8f0', letterSpacing: 1 }}>
          {t('nodeRunner.modals.saveTitle')}
        </div>

        <div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', letterSpacing: 1.5, textTransform: 'uppercase', marginBottom: 6 }}>
            {t('nodeRunner.modals.name')}
          </div>
          <input
            ref={inputRef}
            value={name}
            onChange={e => setName(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') submit(); if (e.key === 'Escape') onClose() }}
            placeholder={t('nodeRunner.modals.namePlaceholder')}
            style={{
              width: '100%', boxSizing: 'border-box',
              background: '#020509',
              border: '1px solid rgba(0,180,216,0.2)',
              borderRadius: 6, padding: '8px 10px',
              color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 12,
              outline: 'none',
            }}
          />
        </div>

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button
            onClick={onClose}
            style={{ background: 'transparent', border: '1px solid rgba(0,180,216,0.15)', borderRadius: 6, padding: '6px 16px', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}
          >{t('nodeRunner.modals.cancel')}</button>
          <button
            onClick={() => { if (name.trim()) submit() }}
            style={{ background: 'rgba(0,180,216,0.15)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 6, padding: '6px 20px', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#00b4d8' }}
          >{t('nodeRunner.modals.save')}</button>
        </div>
      </div>
    </div>
  )
}

// ── Workflows modal ───────────────────────────────────────────────────────────
export function WorkflowsModal({ initialTab = 'saved', currentId, onLoad, onOpenExecution, onDelete, onClose, onLibraryInstalled }) {
  const [tab, setTab]         = useState(initialTab) // 'saved' | 'templates'
  const [list, setList]       = useState([])
  const [loading, setLoading] = useState(true)
  const [listError, setListError] = useState('')
  const [execsError, setExecsError] = useState('')
  const [templates, setTemplates] = useState([])
  const [templatesLoading, setTemplatesLoading] = useState(true)
  const [usingTemplate, setUsingTemplate] = useState(null) // template id being instantiated
  const [execsFor, setExecsFor] = useState(null) // workflowId to show executions for
  const [execs, setExecs]     = useState([])
  const [libraryOpen, setLibraryOpen] = useState(false)
  const [libraryAdded, setLibraryAdded] = useState(null) // last workflow added from monoes.me
  const { t } = useTranslation()

  const loadList = () => {
    setLoading(true); setListError('')
    ListWorkflows().then(d => { setList(d || []); setLoading(false) }).catch(e => { setListError(e?.message || String(e)); setLoading(false) })
  }
  useEffect(() => {
    loadList()
    ListWorkflowTemplates().then(d => { setTemplates(d || []); setTemplatesLoading(false) }).catch(() => setTemplatesLoading(false))
  }, [])

  // Esc closes the modal
  useEffect(() => {
    // The library dialog on top handles its own Escape.
    const onKey = (e) => { if (e.key === 'Escape' && !libraryOpen) onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose, libraryOpen])

  const useTemplate = async (id) => {
    setUsingTemplate(id)
    try {
      const wf = await CreateWorkflowFromTemplate(id)
      onLoad(wf.id)
      onClose()
    } catch (e) {
      setUsingTemplate(null)
      notify(t('nodeRunnerMore.op_createFromTemplate'), e.message || e)
    }
  }

  // Closing the library opens the last workflow added from it.
  const closeLibrary = () => {
    setLibraryOpen(false)
    if (libraryAdded) { onLoad(libraryAdded); onClose() }
  }

  const showExecs = async (id) => {
    setExecsFor(id); setExecsError('')
    try { setExecs(await GetWorkflowExecutions(id)) } catch (e) { setExecs([]); setExecsError(e?.message || String(e)) }
  }

  const errorRow = (msg, onRetry) => (
    <div role="alert" style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#ef4444', padding: '24px', textAlign: 'center' }}>
      <div>{msg}</div>
      <button onClick={onRetry} style={{ marginTop: 10, background: 'rgba(0,180,216,0.08)', border: '1px solid rgba(0,180,216,0.2)', borderRadius: 5, cursor: 'pointer', color: '#00b4d8', padding: '3px 10px', fontFamily: 'var(--font-mono)', fontSize: 10 }}>{t('nodeRunner.modals.retry')}</button>
    </div>
  )

  return (
    <div style={{
      position: 'absolute', inset: 0, zIndex: 100,
      background: 'rgba(2,5,9,0.85)',
      display: 'flex', alignItems: 'center', justifyContent: 'center',
    }} onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div role="dialog" aria-modal="true" aria-label={t('nodeRunner.modals.workflows')} style={{
        width: 520, maxHeight: '70vh',
        background: '#080d16',
        border: '1px solid rgba(0,180,216,0.2)',
        borderRadius: 12,
        display: 'flex', flexDirection: 'column',
        overflow: 'hidden',
        boxShadow: '0 24px 60px rgba(0,0,0,.8)',
      }}>
        {/* Header */}
        <div style={{ padding: '12px 16px', borderBottom: '1px solid rgba(0,180,216,0.1)', display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0 }}>
          {execsFor ? (
            <>
              <button onClick={() => { setExecsFor(null); setExecs([]) }} style={{ background:'transparent',border:'none',cursor:'pointer',color:'var(--text-muted)',display:'flex',alignItems:'center',gap:4,fontFamily:'var(--font-mono)',fontSize:10 }}>
                {t('nodeRunner.modals.back')}
              </button>
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0' }}>{t('nodeRunner.modals.executions')}</span>
            </>
          ) : (
            <>
              <List size={13} color="#00b4d8" />
              <div style={{ display: 'flex', gap: 4, flex: 1 }}>
                <button
                  onClick={() => setTab('saved')}
                  style={{ background: tab === 'saved' ? 'rgba(0,180,216,0.15)' : 'transparent', border: 'none', borderRadius: 5, cursor: 'pointer', color: tab === 'saved' ? '#00b4d8' : 'var(--text-muted)', padding: '4px 10px', fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700 }}
                >{t('nodeRunner.modals.tabSaved')}</button>
                <button
                  onClick={() => setTab('templates')}
                  style={{ background: tab === 'templates' ? 'rgba(0,180,216,0.15)' : 'transparent', border: 'none', borderRadius: 5, cursor: 'pointer', color: tab === 'templates' ? '#00b4d8' : 'var(--text-muted)', padding: '4px 10px', fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700 }}
                >{t('nodeRunner.modals.tabTemplates')}</button>
              </div>
            </>
          )}
          <button onClick={onClose} aria-label={t('nodeRunner.modals.close')} style={{ background:'transparent',border:'none',cursor:'pointer',color:'var(--text-muted)',display:'flex' }}><X size={14} /></button>
        </div>

        {/* Body */}
        <div style={{ flex: 1, overflowY: 'auto', padding: '8px 0' }}>
          {execsFor ? (
            execsError ? errorRow(t('nodeRunner.modals.loadExecsFailed', { error: execsError }), () => showExecs(execsFor)) :
            execs.length === 0 ? (
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', padding: '24px', textAlign: 'center' }}>{t('nodeRunner.modals.noExecs')}</div>
            ) : execs.map(ex => (
              <div key={ex.id} data-exec-row onClick={() => onOpenExecution?.(execsFor, ex.id)}
                style={{ padding: '8px 16px', borderBottom: '1px solid rgba(0,180,216,0.05)', cursor: onOpenExecution ? 'pointer' : 'default' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                  <ExecStatusDot status={ex.status} />
                  <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', flex: 1 }}>{ex.id}</span>
                  <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)' }}>{ex.status}</span>
                  {ex.started_at && <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)' }}>{new Date(ex.started_at).toLocaleString()}</span>}
                </div>
                {ex.error && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: '#ef4444', marginTop: 4, wordBreak: 'break-word' }}>{ex.error}</div>}
              </div>
            ))
          ) : tab === 'templates' ? (<>
            <div style={{ padding: '6px 16px 10px', display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', borderBottom: '1px solid var(--border-dim)' }}>
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', flex: 1 }}>{t('library.workflowMore')}</span>
              <LogInToMonoesButton compact />
              <button className="btn btn-secondary btn-sm" style={{ gap: 5 }} onClick={() => setLibraryOpen(true)}><Library size={11} /> {t('library.browse')}</button>
            </div>
            {templatesLoading ? (
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', padding: '24px', textAlign: 'center' }}>{t('nodeRunner.modals.loading')}</div>
            ) : templates.length === 0 ? (
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', padding: '24px', textAlign: 'center' }}>{t('nodeRunner.modals.noTemplates')}</div>
            ) : templates.map(tpl => (
              <div key={tpl.id} style={{
                padding: '10px 16px', display: 'flex', alignItems: 'center', gap: 10,
                borderBottom: '1px solid rgba(0,180,216,0.05)',
              }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0' }}>{tpl.name}</div>
                  <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', marginTop: 2, whiteSpace: 'normal' }}>
                    {templateBlurb(tpl)}
                  </div>
                </div>
                <button
                  onClick={() => useTemplate(tpl.id)}
                  disabled={usingTemplate === tpl.id}
                  style={{ background:'rgba(0,180,216,0.08)',border:'1px solid rgba(0,180,216,0.2)',borderRadius:5,cursor:'pointer',color:'#00b4d8',padding:'3px 10px',fontFamily:'var(--font-mono)',fontSize:10,flexShrink:0,opacity: usingTemplate === tpl.id ? 0.5 : 1 }}
                  onMouseEnter={e => e.currentTarget.style.background='rgba(0,180,216,0.18)'}
                  onMouseLeave={e => e.currentTarget.style.background='rgba(0,180,216,0.08)'}
                >{usingTemplate === tpl.id ? t('nodeRunner.modals.creating') : t('nodeRunner.modals.useTemplate')}</button>
              </div>
            ))}
          </>) : loading ? (
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', padding: '24px', textAlign: 'center' }}>{t('nodeRunner.modals.loading')}</div>
          ) : listError ? errorRow(t('nodeRunner.modals.loadListFailed', { error: listError }), loadList)
          : list.length === 0 ? (
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', padding: '24px', textAlign: 'center' }}>{t('nodeRunner.modals.noSaved')}</div>
          ) : list.map(wf => (
            <div key={wf.id} style={{
              padding: '10px 16px', display: 'flex', alignItems: 'center', gap: 10,
              borderBottom: '1px solid rgba(0,180,216,0.05)',
              background: wf.id === currentId ? 'rgba(0,180,216,0.05)' : 'transparent',
              cursor: 'pointer',
            }}
              onMouseEnter={e => { if (wf.id !== currentId) e.currentTarget.style.background = 'rgba(255,255,255,0.03)' }}
              onMouseLeave={e => { if (wf.id !== currentId) e.currentTarget.style.background = 'transparent' }}
            >
              <div style={{ width: 7, height: 7, borderRadius: '50%', background: wf.is_active ? '#10b981' : '#334155', flexShrink: 0 }} title={wf.is_active ? t('nodeRunner.modals.active') : t('nodeRunner.modals.inactive')} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{wf.name || t('nodeRunner.modals.untitled')}</div>
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', marginTop: 2 }}>
                  {t('nodeRunner.modals.nodeCount', { count: wf.node_count ?? (wf.nodes || []).length })} · v{wf.version || 1} · {wf.updated_at ? new Date(wf.updated_at).toLocaleString() : ''}
                </div>
              </div>
              <button
                onClick={e => { e.stopPropagation(); showExecs(wf.id) }}
                title={t('nodeRunner.modals.viewExecs')}
                style={{ background:'transparent',border:'none',cursor:'pointer',color:'var(--text-muted)',padding:4,display:'flex',alignItems:'center' }}
                onMouseEnter={e => e.currentTarget.style.color='#00b4d8'}
                onMouseLeave={e => e.currentTarget.style.color='var(--text-muted)'}
              ><List size={11} /></button>
              <button
                onClick={e => { e.stopPropagation(); onLoad(wf.id) }}
                style={{ background:'rgba(0,180,216,0.08)',border:'1px solid rgba(0,180,216,0.2)',borderRadius:5,cursor:'pointer',color:'#00b4d8',padding:'3px 10px',fontFamily:'var(--font-mono)',fontSize:10 }}
                onMouseEnter={e => e.currentTarget.style.background='rgba(0,180,216,0.18)'}
                onMouseLeave={e => e.currentTarget.style.background='rgba(0,180,216,0.08)'}
              >{t('nodeRunner.modals.open')}</button>
              <button
                onClick={e => {
                  e.stopPropagation()
                  // onDelete confirms and resolves false when cancelled — only
                  // drop the row from the list when it was actually deleted.
                  onDelete(wf.id)
                    .then(deleted => { if (deleted) setList(l => l.filter(w => w.id !== wf.id)) })
                    .catch(err => notify(t('nodeRunnerMore.op_deleteWorkflow'), t('nodeRunner.modals.deleteFailed', { error: err?.message || err })))
                }}
                title={t('nodeRunner.modals.delete')}
                style={{ background:'transparent',border:'none',cursor:'pointer',color:'rgba(239,68,68,0.4)',padding:4,display:'flex',alignItems:'center' }}
                onMouseEnter={e => e.currentTarget.style.color='#ef4444'}
                onMouseLeave={e => e.currentTarget.style.color='rgba(239,68,68,0.4)'}
              ><Trash2 size={11} /></button>
            </div>
          ))}
        </div>
      </div>
      {libraryOpen && (
        <LibraryModal kind="workflow" onClose={closeLibrary}
          onInstalled={res => { if (res?.local_id) setLibraryAdded(res.local_id); onLibraryInstalled?.(res) }} />
      )}
    </div>
  )
}

// ── Trigger input modal ───────────────────────────────────────────────────────
// Shown before running a workflow whose nodes read {{ $json.<field> }}. Such a
// workflow is unrunnable without data — the run button used to send none, so
// every expression resolved to nothing and the run did nothing while
// reporting success.
export function TriggerInputModal({ fields, initialValue, onRun, onClose }) {
  const { t } = useTranslation()
  const [text, setText] = useState(initialValue || '{}')
  // A labelled field per key the workflow reads; "Raw JSON" is today's box.
  const [form, setForm] = useState(() => formFromJSON(initialValue || '{}', fields))
  const [raw, setRaw] = useState(() => form === null)
  const [error, setError] = useState('')
  const areaRef = useRef(null)
  useEffect(() => { areaRef.current?.focus(); if (raw) areaRef.current?.select() }, [raw])

  const currentText = () => {
    if (raw) return { ok: true, text }
    return jsonFromForm(form, fields)
  }

  const submit = () => {
    const cur = currentText()
    if (!cur.ok) { setError(cur.error); return }
    const parsed = parseTriggerInput(cur.text)
    if (!parsed.ok) { setError(parsed.error); return }
    onRun(parsed.value)
  }

  const toggleRaw = () => {
    setError('')
    if (raw) {
      const next = formFromJSON(text, fields)
      if (!next) { setError(t('nodeRunner.modals.fixJson')); return }
      setForm(next)
      setRaw(false)
      return
    }
    const cur = jsonFromForm(form, fields)
    if (!cur.ok) { setError(cur.error); return }
    setText(cur.text)
    setRaw(true)
  }

  const onKeyDown = (e) => {
    if (e.key === 'Escape') onClose()
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) submit()
  }
  const boxStyle = {
    width: '100%', boxSizing: 'border-box', resize: 'vertical',
    background: '#020509',
    border: `1px solid ${error ? 'rgba(239,68,68,0.5)' : 'rgba(0,180,216,0.2)'}`,
    borderRadius: 6, padding: '8px 10px',
    color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 12,
    outline: 'none',
  }
  const labelStyle = { fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', letterSpacing: 1.5, textTransform: 'uppercase', marginBottom: 6, display: 'block' }

  return (
    <div style={{
      position: 'absolute', inset: 0, zIndex: 200,
      background: 'rgba(2,5,9,0.8)',
      display: 'flex', alignItems: 'center', justifyContent: 'center',
    }} onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div role="dialog" aria-modal="true" aria-label={t('nodeRunner.modals.runWithInput')} style={{
        width: 440, maxHeight: '85vh', overflowY: 'auto',
        background: '#080d16',
        border: '1px solid rgba(0,180,216,0.25)',
        borderRadius: 12,
        padding: '20px 20px 16px',
        boxShadow: '0 24px 60px rgba(0,0,0,.85)',
        display: 'flex', flexDirection: 'column', gap: 14,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <div style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 700, color: '#e2e8f0', letterSpacing: 1 }}>
            {t('nodeRunner.modals.runWithInputTitle')}
          </div>
          <button onClick={toggleRaw} aria-pressed={raw} style={{
            background: raw ? 'rgba(0,180,216,0.15)' : 'transparent', border: '1px solid rgba(0,180,216,0.2)', borderRadius: 5,
            padding: '3px 8px', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10, color: raw ? '#00b4d8' : 'var(--text-muted)',
          }}>{t('nodeRunner.modals.rawJson')}</button>
        </div>

        <div style={{ fontSize: 11, color: 'var(--text-muted)', lineHeight: 1.5 }}>
          {fields.length === 1
            ? <>{t('nodeRunner.modals.readsPre')} <code style={{ color: '#00b4d8' }}>{fields[0].name}</code> {t('nodeRunner.modals.readsPost')}</>
            : <>{t('nodeRunner.modals.readsPre')} {fields.map((f, i) => (
                <span key={f.name}>
                  {i > 0 && ', '}<code style={{ color: '#00b4d8' }}>{f.name}</code>
                </span>
              ))} {t('nodeRunner.modals.readsPost')}</>}
          {' '}{t('nodeRunner.modals.withoutThem')}
        </div>

        {raw ? (
          <div>
            <label htmlFor="trigger-raw" style={labelStyle}>{t('nodeRunner.modals.triggerData')}</label>
            <textarea
              id="trigger-raw"
              ref={areaRef}
              value={text}
              onChange={e => { setText(e.target.value); if (error) setError('') }}
              onKeyDown={onKeyDown}
              spellCheck={false}
              rows={7}
              style={boxStyle}
            />
          </div>
        ) : fields.map((f, i) => (
          <div key={f.name}>
            <label htmlFor={`trigger-${f.name}`} style={labelStyle}>{f.name}</label>
            <textarea
              id={`trigger-${f.name}`}
              ref={i === 0 ? areaRef : undefined}
              value={form.values[f.name]}
              onChange={e => { const v = e.target.value; setForm(prev => ({ ...prev, values: { ...prev.values, [f.name]: v } })); if (error) setError('') }}
              onKeyDown={onKeyDown}
              spellCheck={!form.asJSON[f.name]}
              rows={form.asJSON[f.name] ? 5 : 3}
              placeholder={f.examples?.[0] || ''}
              style={boxStyle}
            />
            {form.asJSON[f.name] && (
              <div style={{ marginTop: 4, fontSize: 10, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)' }}>
                {t('nodeRunner.modals.arrayHint')}
              </div>
            )}
          </div>
        ))}
        {error && (
          <div role="alert" style={{ marginTop: -6, fontSize: 10, color: '#f87171', fontFamily: 'var(--font-mono)' }}>
            {error}
          </div>
        )}

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <button
            onClick={onClose}
            style={{ background: 'transparent', border: '1px solid rgba(148,163,184,0.25)', borderRadius: 6, padding: '6px 16px', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}
          >{t('nodeRunner.modals.cancel')}</button>
          <button
            onClick={submit}
            style={{ background: 'rgba(0,180,216,0.15)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 6, padding: '6px 20px', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#00b4d8' }}
          >{t('nodeRunner.modals.run')}</button>
        </div>
      </div>
    </div>
  )
}

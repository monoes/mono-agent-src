import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { aitaskApplyApi } from '../SendToCoderApi.js'
import { confirm } from '../ConfirmDialog.jsx'
import SendToCoderButton from '../SendToCoderButton.jsx'
import { selectionText } from '../SendToCoderApi.js'
import { Chip, text, isObj, lineStyle, wrapText, mutedText } from './resultText.jsx'

const YELLOW = 'var(--yellow, #fbbf24)'
const VALUE_CAP = 120

// WARN_KEYS maps the CLI's exact warning codes to the plain wording's key;
// an unknown code shows the CLI's own text.
const WARN_KEYS = { new_outbound: 'sends', credential_field: 'credential', expression_added: 'expression', workflow_active: 'active' }
const ACTIVE_WARNING = 'workflow_active'
const ACTIVE_REFUSAL = 'workflow_active_needs_confirm'

// errParts splits the binding's "code: message" refusal.
export function errParts(e) {
  const raw = String(e?.message || e || '')
  const m = /^([a-z][a-z0-9_]*): ([\s\S]*)$/.exec(raw)
  return m ? { code: m[1], message: m[2] } : { code: '', message: raw }
}

// The review shows at most this many changed leaves and this many characters
// per value; a preview beyond that cannot be reviewed, so it cannot be applied.
const MAX_LEAVES = 50
const MAX_VALUE = 2000
const MAX_PATH = 512
const NOT_CALLED = Symbol('not called')

// looksLikeCredential: a path with a word that names a secret. Words are
// split on case changes and punctuation, so apiKey, api-key and API_KEY all
// read "api key"; credential_id and similar do not match.
const SECRET_WORD = /^(password|passwd|token|secret|authorization|cookie|otp|cvv|apikey|privatekey|accesskey)s?$/
const SECRET_PAIR = new Set(['api', 'private', 'access'])
export function looksLikeCredential(path) {
  const words = String(path ?? '').replace(/([a-z0-9])([A-Z])/g, '$1 $2').toLowerCase().split(/[^a-z0-9]+/).filter(Boolean)
  return words.some((w, i) => SECRET_WORD.test(w) || (SECRET_PAIR.has(w) && /^keys?$/.test(words[i + 1] || '')))
}

const show = (v) => { try { return typeof v === 'string' ? v : JSON.stringify(v) ?? '' } catch { return '' } }

function Value({ v }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const s = show(v)
  const long = s.length > VALUE_CAP
  return (
    <span style={{ overflowWrap: 'anywhere' }}>
      <code>{open ? text(s, MAX_VALUE) : text(s, VALUE_CAP)}</code>
      {long && <button type="button" className="btn btn-ghost btn-sm" onClick={() => setOpen(o => !o)}>{open ? t('taskResult.showLess') : t('taskResult.showMore')}</button>}
    </span>
  )
}

function Edits({ edits }) {
  const { t } = useTranslation()
  const hidden = (
    <span title={t('taskResult.maskedLabel')}>
      <code>••••••</code> <span style={{ color: 'var(--text-muted)' }}>({t('taskResult.maskedLabel')})</span>
    </span>
  )
  if (edits.length === 0) return <span style={mutedText}>{t('taskResult.noEdits')}</span>
  return (
    <ul data-testid="fix-diff" style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4, width: '100%' }}>
      {edits.map((e, i) => (
        <li key={i} style={{ overflowWrap: 'anywhere' }}>
          <strong>{text(e.nodeName || e.node, 80)}</strong> · <code style={{ overflowWrap: 'anywhere', wordBreak: 'break-all' }}>{typeof e.path === 'string' ? e.path : show(e.path)}</code>
          <div><span style={{ color: 'var(--red)' }}>- {looksLikeCredential(e.path) ? hidden : <Value v={e.before} />}</span></div>
          <div><span style={{ color: 'var(--green-neon, #10b981)' }}>+ {looksLikeCredential(e.path) ? hidden : <Value v={e.after} />}</span></div>
        </li>
      ))}
    </ul>
  )
}

// FailedRunFixResult is a completed failed-run-fix task: the diagnosis, and
// for a workflow-edit fix a readable preview of the proposed edits and an
// Apply button. The agent only proposed; the click applies it.
export default function FailedRunFixResult({ taskId, result, onNavigate }) {
  const { t } = useTranslation()
  const fix = isObj(result.fix) ? result.fix : {}
  const editable = fix.kind === 'workflow-edit'
  // The preview and its error are kept with the task id they were asked for;
  // only the current task's are ever used.
  const [previewState, setPreviewState] = useState(null) // { taskId, data }
  const [errState, setErrState] = useState(null) // { taskId, message }
  const preview = previewState?.taskId === taskId ? previewState.data : null
  const previewErr = errState?.taskId === taskId ? errState.message : ''
  const [busy, setBusy] = useState(false)
  const currentTask = useRef(taskId) // the task on screen, for replies that come back late
  currentTask.current = taskId
  const [applied, setApplied] = useState(null)
  const [applyErr, setApplyErr] = useState(null) // { code, message }
  const [done, setDone] = useState(false) // already applied
  const [reload, setReload] = useState(0) // bumped to ask for a fresh preview
  const request = useRef(null) // { key, promise }: one preview request per task and reload, even under StrictMode
  const flight = useRef(null) // the token of the apply in flight

  useEffect(() => {
    if (!editable || !taskId) return undefined
    let live = true
    const asked = taskId
    // A new task starts from nothing: no edits, hash or error of another task.
    setPreviewState(null); setErrState(null); setApplied(null); setApplyErr(null); setDone(false); setBusy(false); flight.current = null
    const key = `${asked}:${reload}`
    if (request.current?.key !== key) request.current = { key, promise: aitaskApplyApi.preview(asked) }
    request.current.promise.then(p => { if (live) setPreviewState({ taskId: asked, data: isObj(p) ? p : { ok: false, edits: [] } }) }).catch(e => { if (live) setErrState({ taskId: asked, message: String(e?.message || e) }) })
    return () => { live = false }
  }, [editable, taskId, reload])

  // reviewAgain drops what was shown and loads the preview anew.
  const reviewAgain = () => { setPreviewState(null); setErrState(null); setApplyErr(null); setReload(n => n + 1) }

  const edits = Array.isArray(preview?.edits) ? preview.edits.filter(isObj) : []
  const warnings = Array.isArray(preview?.warnings) ? preview.warnings.filter(isObj).slice(0, 10) : []
  const workflowId = text(preview?.workflowId || result.workflowId, 200)
  const tooBig = edits.length > MAX_LEAVES || edits.some(e => show(e.path).length > MAX_PATH || show(e.before).length > MAX_VALUE || show(e.after).length > MAX_VALUE)
  const hash = typeof preview?.definitionHash === 'string' ? preview.definitionHash : ''
  const previewOk = !!preview && preview.ok !== false && !preview.error && edits.length > 0 && !tooBig && hash !== ''
  const stalePreview = applyErr?.code === 'changed_since_preview' // the workflow moved on: only a fresh review can be applied
  const canApply = previewOk && !applied && !done && !busy && !stalePreview
  const workflowIsActive = warnings.some(w => w.code === ACTIVE_WARNING)

  // What is on screen right now, read by callApply at the moment of the call.
  const shown = useRef({ taskId: null, hash: '' })
  shown.current = { taskId: previewState?.taskId === taskId ? taskId : null, hash }

  // callApply is the ONLY path to the apply binding (first apply, the retry
  // after an active-workflow confirm, the apply after Review again). Right
  // before calling it re-checks that this apply is the one in flight, that the
  // task on screen is the one asked for, and that the hash is the one of the
  // preview currently shown. Otherwise it calls nothing and returns NOT_CALLED.
  const callApply = (token, asked, allow, reviewed) => {
    const s = shown.current
    if (flight.current !== token || currentTask.current !== asked || s.taskId !== asked || s.hash !== reviewed || reviewed === '') return NOT_CALLED
    return aitaskApplyApi.apply(asked, allow, reviewed)
  }

  const confirmActive = () => confirm(t('taskResult.activeBody'), { title: t('taskResult.activeTitle'), confirmLabel: t('taskResult.activeConfirm') })

  // apply runs only from the Apply button's click: busy is set first, so a
  // second click cannot start a second apply while a dialog or the call is open.
  const apply = async () => {
    if (!canApply || flight.current) return
    const reviewed = hash // the hash of the preview the user is looking at
    const asked = taskId
    if (previewState?.taskId !== asked) return
    const token = {}
    flight.current = token
    setBusy(true); setApplyErr(null)
    try {
      const nodes = new Set(edits.map(e => e.node)).size
      if (!(await confirm(t('taskResult.applyBody', { fields: t('taskResult.fieldCount', { count: edits.length }), nodes: t('taskResult.nodeCount', { count: nodes }), name: text(preview.workflowName || workflowId, 80) }), { title: t('taskResult.applyTitle'), confirmLabel: t('taskResult.applyConfirm'), danger: false }))) return
      let allow = false
      if (workflowIsActive) {
        if (!(await confirmActive())) return
        allow = true
      }
      let r
      try {
        r = await callApply(token, asked, allow, reviewed)
      } catch (e) {
        const p = errParts(e)
        // The CLI refused an active workflow the preview did not flag: ask, then retry with the flag.
        if (!allow && p.code === ACTIVE_REFUSAL && (await confirmActive())) r = await callApply(token, asked, true, reviewed)
        else throw e
      }
      if (r === NOT_CALLED) return // the screen changed meanwhile; nothing was applied
      if (currentTask.current !== asked) return
      setApplied({ count: Number.isFinite(r?.applied) ? r.applied : edits.length, workflowId: text(r?.workflowId || workflowId, 200) })
    } catch (e) {
      if (currentTask.current !== asked) return
      const p = errParts(e)
      if (p.code === 'already_applied') setDone(true)
      else setApplyErr(p)
    } finally { if (flight.current === token) flight.current = null; if (currentTask.current === asked) setBusy(false) }
  }

  const errorLine = (() => {
    if (!applyErr) return null
    if (applyErr.code === 'stale_proposal') return t('taskResult.stale')
    if (applyErr.code === 'changed_since_preview') return t('taskResult.changedSincePreview')
    if (applyErr.code === 'too_large') return t('taskResult.tooLarge')
    if (applyErr.code === 'operator_only') return t('taskResult.operatorOnly')
    if (applyErr.code === ACTIVE_REFUSAL) return t('taskResult.activeDeclined')
    return t('taskResult.applyError', { error: text(applyErr.message, 300) })
  })()

  return (
    <div data-testid="task-result" style={{ ...lineStyle, alignItems: 'flex-start' }}>
      <span style={wrapText}>{text(result.diagnosis, 600)}</span>
      {fix.description && <span style={{ ...mutedText, width: '100%' }}>{text(fix.description, 400)}</span>}
      {fix.kind === 'advice' && <span style={mutedText}>{t('taskResult.adviceOnly')}</span>}
      {editable && !preview && !previewErr && <span style={mutedText}>{t('taskResult.previewLoading')}</span>}
      {previewErr && <span role="alert" style={{ color: 'var(--red)', width: '100%' }}>{t('taskResult.previewError', { error: text(previewErr, 300) })}</span>}
      {preview && <Edits edits={edits} />}
      {preview && tooBig && <span role="alert" style={{ color: 'var(--red)', width: '100%' }}>{t('taskResult.tooBigToReview')}</span>}
      {preview && preview.ok === false && <span role="alert" style={{ color: 'var(--red)', width: '100%' }}>{text(preview.error, 300) || t('taskResult.previewInvalid')}</span>}
      {warnings.map((w, i) => <Chip key={i} tone={YELLOW} title={text(w.text, 200)}>{WARN_KEYS[w.code] ? t(`taskResult.warn.${WARN_KEYS[w.code]}`) : text(w.text || w.code, 120)}</Chip>)}
      {((editable && !applied && !done) || done) && (
        <div data-testid="apply-row" style={{ width: '100%', display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          {done
            ? <button type="button" className="btn btn-primary btn-sm" disabled>{t('taskResult.alreadyApplied')}</button>
            : <button type="button" className="btn btn-primary btn-sm" onClick={apply} disabled={!canApply}>{busy ? t('taskResult.applying') : t('taskResult.apply')}</button>}
        </div>
      )}
      {applied && (
        <span role="status" style={{ ...wrapText, width: '100%' }}>
          {t('taskResult.applied', { count: applied.count })}
          {applied.workflowId && onNavigate && <> <button type="button" className="btn btn-ghost btn-sm" onClick={() => onNavigate('noderunner', { workflowId: applied.workflowId })}>{t('taskResult.openWorkflow')}</button></>}
        </span>
      )}
      {errorLine && <span role="alert" style={{ color: 'var(--red)', width: '100%' }}>{errorLine}</span>}
      {applyErr?.code === 'changed_since_preview' && <button type="button" className="btn btn-secondary btn-sm" onClick={reviewAgain}>{t('taskResult.reviewAgain')}</button>}
      {(applyErr?.code === 'stale_proposal' || applyErr?.code === 'too_large') && (
        <SendToCoderButton kind="failed-run-fix" label={t('taskResult.askAgain')} goal="Work out why this workflow run failed and propose a fix."
          context={[selectionText(`workflow: ${text(result.workflowId, 200)}\nexecution: ${text(result.executionId, 200)}`)]}
          origin={{ page: 'noderunner', label: 'Workflows' }} />
      )}
    </div>
  )
}

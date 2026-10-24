import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { aitaskApplyApi } from '../SendToCoderApi.js'
import FailedRunFixResult from './FailedRunFixResult.jsx'
import { Chip, text, isObj, lineStyle, wrapText, mutedText } from './resultText.jsx'

const YELLOW = 'var(--yellow, #fbbf24)'
const list = (v, n) => (Array.isArray(v) ? v.filter(x => typeof x === 'string').slice(0, n) : [])

function GeneratedWorkflow({ taskId, result, onNavigate }) {
  const { t } = useTranslation()
  const wf = isObj(result.workflow) ? result.workflow : {}
  const v = isObj(result.validation) ? result.validation : {}
  const errors = list(v.errors, 20)
  const warnings = list(v.warnings, 20)
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState(null)
  const [err, setErr] = useState('')
  const nodes = Array.isArray(wf.nodes) ? wf.nodes.length : 0
  const save = async () => {
    setBusy(true); setErr('')
    try { const r = await aitaskApplyApi.saveDraft(taskId); setSaved({ id: text(r?.workflowId, 200) }) } catch (e) { setErr(String(e?.message || e)) } finally { setBusy(false) }
  }
  return (
    <div data-testid="task-result" style={lineStyle}>
      <span style={wrapText}>{t('taskResult.created', { name: text(wf.name, 120) || '…', count: nodes })}</span>
      {v.valid === true && <Chip tone="var(--green-neon, #10b981)">{t('taskResult.valid')}</Chip>}
      {v.valid === false && <Chip tone="var(--red)" title={errors.map(e => text(e, 200)).join('\n')}>{t('taskResult.hasErrors', { count: errors.length || 1 })}</Chip>}
      {warnings.length > 0 && <Chip tone={YELLOW} title={warnings.map(e => text(e, 200)).join('\n')}>{t('taskResult.hasWarnings', { count: warnings.length })}</Chip>}
      {result.summary && <span style={{ ...mutedText, width: '100%' }}>{text(result.summary, 400)}</span>}
      {!saved && <button type="button" className="btn btn-primary btn-sm" onClick={save} disabled={busy || v.valid !== true}>{busy ? t('taskResult.saving') : t('taskResult.saveDraft')}</button>}
      {saved && (
        <span role="status" style={wrapText}>
          {t('taskResult.saved')}
          {saved.id && onNavigate && <> <button type="button" className="btn btn-ghost btn-sm" onClick={() => onNavigate('noderunner', { workflowId: saved.id })}>{t('taskResult.openWorkflow')}</button></>}
        </span>
      )}
      {err && <span role="alert" style={{ color: 'var(--red)', width: '100%' }}>{t('taskResult.saveError', { error: text(err, 300) })}</span>}
    </div>
  )
}

function RecordingResult({ result }) {
  const { t } = useTranslation()
  const suggestions = list(result.suggestions, 12)
  const risks = list(result.risks, 12)
  return (
    <div data-testid="task-result" style={lineStyle}>
      <span style={wrapText}>{text(result.summary, 400)}</span>
      {suggestions.length > 0 && (
        <div style={{ width: '100%' }}>
          <span style={mutedText}>{t('taskResult.suggestions')}</span>
          <ul style={{ margin: '2px 0 0', paddingLeft: 18 }}>{suggestions.map((s, i) => <li key={i} style={{ overflowWrap: 'anywhere' }}>{text(s, 300)}</li>)}</ul>
        </div>
      )}
      {risks.length > 0 && (
        <div style={{ ...lineStyle, gap: 6 }} aria-label={t('taskResult.risks')}>
          <span style={mutedText}>{t('taskResult.risks')}</span>
          {risks.map((r, i) => <Chip key={i} tone={YELLOW}>{text(r, 120)}</Chip>)}
        </div>
      )}
    </div>
  )
}

// TaskResultLine is the kind-specific result under a completed task's header
// (#958). The agent only proposes: applying a fix or saving a generated
// workflow is the user's click. Unknown kinds and a malformed result render
// nothing.
export function TaskResultLine({ kind, taskId, result, onNavigate }) {
  if (!isObj(result)) return null
  if (kind === 'workflow-generate') return <GeneratedWorkflow taskId={taskId} result={result} onNavigate={onNavigate} />
  if (kind === 'recording-analyze') return <RecordingResult result={result} />
  if (kind === 'failed-run-fix') return <FailedRunFixResult key={taskId} taskId={taskId} result={result} onNavigate={onNavigate} />
  return null
}

import { AlertCircle, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import '../i18n.js'
import { withoutAgentSetupMarker } from '../lib/agentSetup.js'
import SendToCoderButton from './SendToCoderButton.jsx'
import { selectionText } from './SendToCoderApi.js'

// problemText is the failure as plain text for the coder: the run's error and
// each failed node with its error.
export function problemText(problem) {
  const lines = []
  if (problem.error) lines.push(withoutAgentSetupMarker(problem.error))
  for (const f of problem.failed) lines.push(`${f.name}: ${withoutAgentSetupMarker(f.error || 'failed')}`)
  return lines.join('\n')
}

const ERROR_CAP = 500
const CONTEXT_CAP = 4096
const clip = (text, n) => (text.length > n ? `${text.slice(0, n - 1)}…` : text)

// failedRunContext is what a failed-run-fix task is given: the workflow and
// execution ids, the failing node ids and each error text (500 chars each,
// 4 KB in all). Never a node's output payload.
export function failedRunContext(problem, workflowId, executionId) {
  const lines = [`workflow: ${workflowId || ''}`, `execution: ${executionId || ''}`]
  if (problem.error) lines.push(`run error: ${clip(withoutAgentSetupMarker(problem.error), ERROR_CAP)}`)
  for (const f of problem.failed) lines.push(`node ${f.id || ''} (${f.name}): ${clip(withoutAgentSetupMarker(f.error || 'failed'), ERROR_CAP)}`)
  let out = ''
  for (const line of lines) {
    const next = out ? `${out}\n${line}` : line
    if (new TextEncoder().encode(next).length > CONTEXT_CAP) break
    out = next
  }
  return out
}

// The "why did it fail" panel under the editor toolbar: the execution's own
// error and each failed node's. problem is runProblem() from
// lib/runFailure.js; clicking a node row selects that node on the canvas.
// onSendToCoder, when given, adds "Fix with coder": it is called with the
// failure text and returns { goal, origin } (how to frame the task and which
// tab it came from); the failure itself goes to the coder as selection text.
export default function RunProblemPanel({ problem, onSelectNode, onClose, onSendToCoder, workflowId, executionId }) {
  const { t } = useTranslation()
  if (!problem) return null
  const failed = problem.status === 'FAILED'
  const tone = failed ? '#ef4444' : '#fbbf24'
  const nodeErrors = problem.failed.filter(f => f.error && f.error !== problem.error)
  const coder = onSendToCoder ? onSendToCoder(problemText(problem)) : null
  return (
    <div role="alert" data-testid="run-problem" style={{
      display: 'flex', gap: 10, alignItems: 'flex-start',
      padding: '10px 14px', background: failed ? 'rgba(239,68,68,0.08)' : 'rgba(251,191,36,0.08)',
      borderBottom: `1px solid ${failed ? 'rgba(239,68,68,0.3)' : 'rgba(251,191,36,0.3)'}`,
      fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5', flexShrink: 0,
    }}>
      <AlertCircle size={14} color={tone} style={{ flexShrink: 0, marginTop: 1 }} />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ color: tone, fontWeight: 700, marginBottom: 3 }}>
          {failed ? t('nodeRunnerMore.runFailed') : t('ui6Node.runWithErrors')}
          {problem.failed.length > 0 && <span style={{ fontWeight: 400, color: 'var(--text-muted)' }}>
            {' '}· {t('ui6Node.marked', { count: problem.failed.length })}
          </span>}
        </div>
        {problem.error && (
          <div style={{ color: '#fecaca', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{withoutAgentSetupMarker(problem.error)}</div>
        )}
        {nodeErrors.map(f => (
          <button key={f.id} onClick={() => onSelectNode?.(f.id)} style={{
            display: 'block', width: '100%', textAlign: 'left', marginTop: 4, padding: 0,
            background: 'none', border: 'none', cursor: 'pointer',
            fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fecaca', whiteSpace: 'pre-wrap', wordBreak: 'break-word',
          }}>
            <span style={{ color: tone, textDecoration: 'underline' }}>{f.name}</span>: {withoutAgentSetupMarker(f.error)}
          </button>
        ))}
      </div>
      {coder && (
        <SendToCoderButton kind="failed-run-fix" label={t('ui6Node.fixWithCoder')} goal={coder.goal} origin={coder.origin}
          context={() => [selectionText(failedRunContext(problem, workflowId, executionId))]} />
      )}
      <button onClick={onClose} aria-label={t('ui6Node.hideError')} title={t('ui6Node.hideError')} style={{
        background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 2, display: 'flex',
      }}><X size={12} /></button>
    </div>
  )
}

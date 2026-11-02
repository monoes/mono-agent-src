import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Wrench } from 'lucide-react'
import { confirm } from './ConfirmDialog.jsx'
import { runFix } from '../lib/health.js'

// The doctor fix that installs or repairs monomind (health.FixMonomindInstall);
// the CLI runs it, this only asks first and shows the outcome.
export const FIX_MONOMIND_ID = 'monomind.install'
// What the fix runs, named in the confirmation. Installing an npm package
// runs code from the registry, so the person sees the command before it runs.
export const FIX_MONOMIND_COMMAND = 'monoagentcli doctor fix monomind.install  (runs: npm install -g @monoes/monomindcli@latest)'
const MAX_LINES = 6

// FixMonomindButton: "Fix monomind" next to a message saying monomind could
// not be used or is outdated. Confirm, run through the CLI with progress,
// then onDone() so the caller rescans.
export default function FixMonomindButton({ onDone }) {
  const { t } = useTranslation()
  const [state, setState] = useState({ phase: 'idle', lines: [], error: '' })

  const start = async () => {
    const ok = await confirm(
      <span>
        {t('monomindFix.confirmBody')}
        <code style={{ display: 'block', marginTop: 8, padding: '6px 8px', background: 'rgba(0,0,0,.35)', borderRadius: 4, wordBreak: 'break-all' }}>
          {FIX_MONOMIND_COMMAND}
        </code>
      </span>,
      { title: t('monomindFix.action'), confirmLabel: t('monomindFix.run'), cancelLabel: t('monomindFix.cancel'), danger: false },
    )
    if (!ok) return
    setState({ phase: 'running', lines: [], error: '' })
    const res = await runFix(FIX_MONOMIND_ID, line => setState(s => ({ ...s, lines: [...s.lines, line].slice(-MAX_LINES) })))
    if (res.ok) {
      setState({ phase: 'done', lines: [], error: '' })
      onDone?.()
    } else {
      setState(s => ({ ...s, phase: 'error', error: res.message || 'failed' }))
    }
  }

  const running = state.phase === 'running'
  return (
    <span data-testid="fix-monomind" style={{ display: 'inline-flex', flexDirection: 'column', gap: 4, marginTop: 6 }}>
      <button type="button" className="btn btn-secondary btn-sm" onClick={start} disabled={running}
        style={{ display: 'inline-flex', alignItems: 'center', gap: 5, alignSelf: 'flex-start' }}>
        <Wrench size={11} /> {running ? t('monomindFix.running') : t('monomindFix.action')}
      </button>
      {state.lines.length > 0 && running && (
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>
          {state.lines.map((l, i) => <span key={i} style={{ display: 'block', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{l}</span>)}
        </span>
      )}
      {state.phase === 'done' && <span role="status" style={{ fontSize: 10, color: 'var(--green-neon)' }}>{t('monomindFix.done')}</span>}
      {state.phase === 'error' && <span role="alert" style={{ fontSize: 10, color: '#f87171', wordBreak: 'break-word' }}>{t('monomindFix.failed', { why: state.error })}</span>}
    </span>
  )
}

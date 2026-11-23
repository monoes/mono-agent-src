import { useTranslation } from 'react-i18next'
import { Wrench, Loader2, Copy, Bot, X } from 'lucide-react'
import { fixPlan } from '../../lib/health.js'

// "Finish setup": the problems System health can resolve, as numbered
// steps in the order `monoagentcli setup` would take them — automatic and
// confirmed fixes (run in turn by the button), steps the user does by hand,
// and, when no AI agent runtime is installed, a pointer to install one.

const mono = { fontFamily: 'var(--font-mono)' }

/** The ordered steps for a report (exported for tests). */
export function setupSteps(report) {
  const steps = fixPlan(report).map(f => ({ kind: 'fix', fix: f, title: f.rowTitle }))
  const seen = new Set(steps.map(s => s.fix.id))
  for (const r of report?.results || []) {
    const f = r.fix
    if (f?.safety === 'manual' && (r.status === 'warn' || r.status === 'fail') && !seen.has(f.id)) {
      seen.add(f.id)
      steps.push({ kind: 'manual', fix: f, title: r.title })
    }
  }
  const runtimes = (report?.results || []).find(r => r.id === 'runtimes.agents')
  if (runtimes && runtimes.status === 'fail') steps.push({ kind: 'runtime', title: runtimes.title })
  return steps
}

function StepMark({ step, state, n }) {
  if (state?.running) return <Loader2 size={13} className="spin" style={{ color: 'var(--cyan)' }} />
  if (state?.done) return <span style={{ color: 'var(--green-neon)' }}>✓</span>
  if (state?.error) return <span style={{ color: 'var(--red)' }}>✗</span>
  if (state?.cancelled) return <span style={{ color: 'var(--text-muted)' }}>–</span>
  return <span style={{ color: step.kind === 'fix' ? 'var(--cyan)' : 'var(--text-muted)' }}>{n}</span>
}

// fixingAll: the steps are running (label says so); busy: something else
// runs (a check or a single fix), so the button waits.
export default function SetupSteps({ report, level, fixStates, fixingAll, busy, onRunAll, onCancelAll, onFix, onNavigate }) {
  const { t } = useTranslation()
  const steps = setupSteps(report)
  if (steps.length === 0) return null
  const runnable = steps.filter(s => s.kind === 'fix').length

  return (
    <div data-testid="setup-steps" style={{ background: 'var(--surface)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius-lg)', padding: '12px 18px', marginBottom: 12 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
        <span style={{ ...mono, fontSize: 12, color: 'var(--text)', flex: 1 }}>
          {level === 'broken' ? t('settings.health.finishSetup') : t('settings.health.stepsToFix')}
        </span>
        {runnable > 0 && (
          <button
            onClick={onRunAll}
            disabled={fixingAll || busy}
            style={{ ...mono, fontSize: 10, display: 'inline-flex', alignItems: 'center', gap: 6, padding: '5px 12px', borderRadius: 5, border: 'none', background: 'var(--cyan)', color: '#fff', cursor: fixingAll || busy ? 'default' : 'pointer', opacity: fixingAll || busy ? 0.6 : 1 }}
          >
            {fixingAll ? <Loader2 size={12} className="spin" /> : <Wrench size={12} />}
            {fixingAll ? t('settings.health.fixing') : t('settings.health.fixIssues', { n: runnable })}
          </button>
        )}
        {fixingAll && onCancelAll && (
          <button
            onClick={onCancelAll}
            style={{ ...mono, fontSize: 10, display: 'inline-flex', alignItems: 'center', gap: 6, padding: '5px 12px', borderRadius: 5, background: 'transparent', color: 'var(--text-secondary)', border: '1px solid var(--border)', cursor: 'pointer' }}
          >
            <X size={12} />{t('settings.health.cancel')}
          </button>
        )}
      </div>
      <ol style={{ listStyle: 'none', margin: 0, padding: 0 }}>
        {steps.map((s, i) => {
          const state = s.fix ? fixStates[s.fix.id] : null
          return (
            <li key={s.fix?.id || s.kind} data-step={s.fix?.id || s.kind} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '5px 0', borderTop: i ? '1px solid var(--border)' : 'none' }}>
              <span style={{ ...mono, fontSize: 11, width: 16, textAlign: 'center' }}><StepMark step={s} state={state} n={i + 1} /></span>
              <span style={{ ...mono, fontSize: 11, color: 'var(--text)', flex: 1, minWidth: 0 }}>
                {s.kind === 'runtime' ? t('settings.health.stepRuntime') : s.fix.label}
                <span style={{ color: 'var(--text-muted)', marginLeft: 8 }}>
                  {s.kind === 'manual' ? t('settings.health.stepManual') : s.kind === 'fix' && s.fix.safety === 'confirm' ? t('settings.health.stepAsks') : s.kind === 'fix' ? t('settings.health.stepAuto') : ''}
                </span>
                {state?.error && <span style={{ color: 'var(--red)', marginLeft: 8 }}>{state.error.split('\n')[0]}</span>}
              </span>
              {s.kind === 'manual' && (
                <button onClick={() => onFix(s.fix)} title={s.fix.command} style={{ ...mono, fontSize: 10, display: 'inline-flex', alignItems: 'center', gap: 5, padding: '3px 10px', borderRadius: 4, background: 'transparent', color: 'var(--text-secondary)', border: '1px solid var(--border)', cursor: 'pointer' }}>
                  <Copy size={11} />{t('settings.health.copyCommand')}
                </button>
              )}
              {s.kind === 'runtime' && onNavigate && (
                <button onClick={() => onNavigate('ai')} style={{ ...mono, fontSize: 10, display: 'inline-flex', alignItems: 'center', gap: 5, padding: '3px 10px', borderRadius: 4, background: 'transparent', color: 'var(--cyan)', border: '1px solid var(--border-bright)', cursor: 'pointer' }}>
                  <Bot size={11} />{t('settings.health.openAgentsShort')}
                </button>
              )}
            </li>
          )
        })}
      </ol>
    </div>
  )
}

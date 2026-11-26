// Review a recording (spec §8.6): `record analyze` drafts an action with
// AI-proposed names, `record verify` replays it (safe mode stops before the
// first side-effecting step), `record save` stores it as an action,
// fragment or workflow. Names are editable before saving.
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '../../i18n.js'
import { ArrowLeft, ShieldCheck, Play, Save, Sparkles, AlertTriangle } from 'lucide-react'
import { api } from '../../services/api.js'
import { confirm } from '../../components/ConfirmDialog.jsx'
import { Chip, EffectChip, ErrorBox, OkBox, Busy, isRisky, body, label, mono, muted, panel } from './ui.jsx'
import { ScriptSources } from './ImportReview.jsx'
import { IssueList } from './OverviewTab.jsx'
import SendToCoderButton from '../../components/SendToCoderButton.jsx'
import { selectionText } from '../../components/SendToCoderApi.js'

const VERIFY_COLORS = { pass: 'var(--green-neon)', healed: 'var(--cyan)', fail: 'var(--red)', stopped_before_side_effect: 'var(--yellow)', skipped: 'var(--text-muted)' }

// scriptRefused: a verify step failed because page scripts are off for this
// trust tier (drafts verify as "recorded"). Prefers the CLI's code; the
// message match covers CLIs that do not send one yet.
export function scriptRefused(step) {
  if (!step || step.status !== 'fail') return false
  return step.code === 'scripts_refused' || /scripts are not allowed/i.test(step.message || '')
}
// outputList flattens ActionDef.outputs ({success: [...], ...}) or a plain list.
function outputList(o) {
  if (Array.isArray(o)) return o
  if (!o || typeof o !== 'object') return []
  return [...new Set(Object.values(o).flat().filter(x => typeof x === 'string'))]
}

const inputStyle = { ...mono, fontSize: 11, background: 'var(--surface)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '5px 8px', minWidth: 0 }

function Field({ label: text, value, onChange, id }) {
  return (
    <label htmlFor={id} style={{ display: 'flex', flexDirection: 'column', gap: 4, flex: 1, minWidth: 160 }}>
      <span style={label}>{text}</span>
      <input id={id} value={value} onChange={e => onChange(e.target.value)} style={inputStyle} />
    </label>
  )
}

function StepList({ steps, verify }) {
  const { t } = useTranslation()
  const byId = Object.fromEntries((verify?.steps || []).map(s => [s.id, s]))
  return (
    <ol style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 5 }}>
      {steps.map((s, i) => {
        const v = byId[s.id]
        return (
          <li key={s.id || i} style={{ display: 'flex', gap: 8, alignItems: 'baseline', borderTop: i ? '1px solid var(--border-dim)' : 'none', paddingTop: i ? 5 : 0 }}>
            <span style={{ ...mono, fontSize: 10, color: 'var(--text-dim)', minWidth: 18, textAlign: 'right' }}>{i + 1}</span>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
                <span style={{ ...mono, fontSize: 10.5, color: 'var(--cyan)' }}>{s.type}</span>
                {s.sideEffect && <Chip color="var(--orange)">{t('ui6ConnAuto.side_effect')}</Chip>}
                {v && <Chip color={VERIFY_COLORS[v.status] || 'var(--text-muted)'}>{t(`ui6ConnAuto.verify_value_${v.status}`, { defaultValue: v.status })}</Chip>}
              </div>
              <div style={{ ...body, fontSize: 11 }}>{s.intent || s.description || s.text || s.configKey || s.url || s.id}</div>
              {v?.message && <div style={{ ...muted, fontSize: 10, color: v.status === 'fail' ? 'var(--red)' : 'var(--text-muted)' }}>{v.message}</div>}
              {(v?.selector || s.configKey) && <div style={{ ...muted, fontSize: 9.5, wordBreak: 'break-all' }}>{v?.selector || s.configKey}</div>}
            </div>
          </li>
        )
      })}
    </ol>
  )
}

export default function RecordingReview({ recording, automationId, onBack, onSaved }) {
  const { t } = useTranslation()
  const [phase, setPhase] = useState('analyzing') // analyzing | ready | verifying | saving | saved
  const [error, setError] = useState('') // analyze
  const [verifyError, setVerifyError] = useState('')
  const [saveError, setSaveError] = useState('')
  const [forceAsk, setForceAsk] = useState(false) // lint errors shown, waiting for "Save anyway"
  const [draftDir, setDraftDir] = useState('')
  const [draft, setDraft] = useState(null)
  const [verify, setVerify] = useState(null)
  const [saved, setSaved] = useState(null)
  const [names, setNames] = useState({ action: '', automation: '', fragment: '' })
  const [inputNames, setInputNames] = useState({})
  const [saveAs, setSaveAs] = useState('action')
  const [attempt, setAttempt] = useState(0) // bump to re-run analyze
  const [advanced, setAdvanced] = useState(false) // --allow-advanced
  const [verifyInputs, setVerifyInputs] = useState({}) // values the recording could not hold

  useEffect(() => {
    let live = true
    setPhase('analyzing'); setError(''); setVerify(null); setVerifyError(''); setSaveError(''); setSaved(null); setForceAsk(false)
    ;(async () => {
      const res = await api.analyzeRecording(recording.id, automationId, advanced)
      if (!live) return
      if (!res || res.error) { setError(res?.error || i18n.t('ui6ConnAuto.analysis_failed')); setPhase('ready'); return }
      const d = res.draft || {}
      setDraftDir(res.draftDir)
      setDraft(d)
      setNames({ action: d.names?.action || d.action || '', automation: d.names?.automation || d.targetAutomation || automationId, fragment: d.names?.fragment || '' })
      setInputNames(Object.fromEntries((d.inputs || []).map(i => [i.name, i.name])))
      setSaveAs(d.saveAs || 'action')
      setPhase('ready')
    })()
    return () => { live = false }
  }, [recording.id, automationId, attempt, advanced])

  const def = draft?.actionDef || {}
  const steps = def.steps || []
  const inputs = draft?.inputs || []
  const outputs = outputList(def.outputs)
  const recorded = draft?.recordedInputs || {}
  // The CLI marks inputs verify needs a value for (secrets are never
  // recorded; required inputs with no recorded value or default).
  const missing = inputs.filter(i => i.needsValue)

  const reanalyzeAdvanced = async () => {
    if (!(await confirm(t('ui6ConnAuto.advanced_confirm'), { confirmLabel: t('ui6ConnAuto.reanalyze') }))) return
    setDraft(null)
    setAdvanced(true)
  }

  const runVerify = async (full) => {
    if (full && isRisky(def.sideEffects) && !(await confirm(def.sideEffects
      ? t('ui6ConnAuto.full_run_effects', { effect: t(`ui6Conn.effect_value_${def.sideEffects}`, { defaultValue: def.sideEffects }) })
      : t('ui6ConnAuto.full_run_unknown')))) return
    setPhase('verifying'); setVerifyError('')
    const res = await api.verifyDraft(draftDir, full, verifyInputs)
    if (!res || (res.error && !res.steps)) setVerifyError(res?.error || t('ui6ConnAuto.verify_failed'))
    else { setVerify(res); if (res.error) setVerifyError(res.error) }
    setPhase('ready')
  }

  const lintErrors = (draft?.lint || []).filter(i => i.severity === 'error')

  // save refuses a draft with error-level lint until the user has seen the
  // errors and chosen "Save anyway", which passes --force.
  const save = async (force = false) => {
    if (lintErrors.length && !force) { setForceAsk(true); return }
    setForceAsk(false)
    setPhase('saving'); setSaveError('')
    const renameInputs = Object.fromEntries(Object.entries(inputNames).filter(([from, to]) => to && to !== from))
    const spec = {
      as: saveAs,
      name: saveAs === 'fragment' ? names.fragment || names.action : names.action,
      renameInputs,
      ...(draft?.isNew ? { new: names.automation } : { automation: names.automation || automationId }),
      ...(force ? { force: true } : {}),
    }
    const res = await api.saveDraft(draftDir, spec)
    if (!res || res.error) { setSaveError(res?.error || t('ui6ConnAuto.save_failed')); setPhase('ready'); return }
    setSaved(res); setPhase('saved')
    onSaved?.()
  }

  const busy = phase === 'analyzing' || phase === 'verifying' || phase === 'saving'
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <button className="btn btn-ghost btn-sm" onClick={onBack} style={{ gap: 5 }}><ArrowLeft size={11} /> {t('ui6ConnAuto.tab_Recordings')}</button>
        <span title={recording.title || recording.id} style={{ ...mono, fontSize: 11.5, color: 'var(--text)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{recording.title || recording.id}</span>
        <SendToCoderButton kind="recording-analyze" label={t('ui6ConnAuto.discuss')} goal="Review this recording and the action drafted from it: check the steps and inputs, and suggest improvements."
          context={[selectionText(`recording: ${recording.id}${recording.title ? `\ntitle: ${String(recording.title).replace(/\s+/g, ' ').slice(0, 200)}` : ''}`)]}
          origin={{ page: 'connections', label: t('ui6Conn.page_title') }} />
      </div>
      {phase === 'analyzing' && <Busy text={t('ui6ConnAuto.analyzing')} />}
      <ErrorBox>{error}</ErrorBox>
      {!draft && error && phase !== 'analyzing' && (
        <button className="btn btn-secondary btn-sm" onClick={() => setAttempt(n => n + 1)} style={{ alignSelf: 'flex-start', gap: 5 }}><Sparkles size={11} /> {t('ui6ConnAuto.analyze_again')}</button>
      )}

      {draft && (
        <>
          <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <Sparkles size={12} color="var(--cyan)" />
              <span style={{ ...label, flex: 1 }}>{t('ui6ConnAuto.names_proposed')}</span>
              {def.sideEffects && <EffectChip effect={def.sideEffects} />}
            </div>
            {def.description && <div style={{ ...body, fontSize: 11 }}>{def.description}</div>}
            <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
              <Field id="rr-action" label={t('ui6ConnAuto.action_name')} value={names.action} onChange={v => setNames(n => ({ ...n, action: v }))} />
              {draft.isNew
                ? <Field id="rr-automation" label={t('ui6ConnAuto.new_automation_id')} value={names.automation} onChange={v => setNames(n => ({ ...n, automation: v }))} />
                : <div style={{ display: 'flex', flexDirection: 'column', gap: 4, flex: 1, minWidth: 160 }}><span style={label}>{t('ui6ConnAuto.saves_into')}</span><span style={{ ...mono, fontSize: 11, color: 'var(--text)', padding: '5px 0' }}>{names.automation}</span></div>}
              {saveAs === 'fragment' && <Field id="rr-fragment" label={t('ui6ConnAuto.fragment_name')} value={names.fragment} onChange={v => setNames(n => ({ ...n, fragment: v }))} />}
            </div>
            {Object.keys(inputNames).length > 0 && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                <span style={label}>{t('ui6ConnAuto.inputs')}</span>
                {inputs.map(i => (
                  <div key={i.name} style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                    <input aria-label={t('ui6ConnAuto.name_for_input', { name: i.name })} value={inputNames[i.name] ?? i.name} onChange={e => setInputNames(m => ({ ...m, [i.name]: e.target.value }))} style={{ ...inputStyle, width: 180 }} />
                    <span style={{ ...mono, fontSize: 10, color: 'var(--cyan)' }}>{i.type || 'string'}</span>
                    <span style={{ ...muted, fontSize: 10, flex: 1, minWidth: 0, wordBreak: 'break-word' }}>{i.description || (recorded[i.name] !== undefined ? t('ui6ConnAuto.recorded_value', { value: JSON.stringify(recorded[i.name]) }) : i.default != null ? t('ui6ConnAuto.default_colon', { value: JSON.stringify(i.default) }) : '')}</span>
                  </div>
                ))}
              </div>
            )}
            {outputs.length > 0 && (
              <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', alignItems: 'center' }}>
                <span style={{ ...label, marginRight: 4 }}>{t('ui6ConnAuto.outputs')}</span>
                {outputs.map(o => <Chip key={o} color="var(--teal)">{o}</Chip>)}
              </div>
            )}
          </div>

          <IssueList issues={draft.lint} />
          <ScriptSources sources={draft.scripts} title={t('ui6ConnAuto.ai_scripts')} />
          {!advanced && (
            <div style={{ ...panel, display: 'flex', gap: 10, alignItems: 'center' }}>
              <AlertTriangle size={14} color="var(--yellow)" style={{ flexShrink: 0 }} />
              <span style={{ ...body, fontSize: 11, flex: 1 }}>{t('ui6ConnAuto.declarative_note')}</span>
              <button className="btn btn-ghost btn-sm" onClick={reanalyzeAdvanced} disabled={busy}>{t('ui6ConnAuto.allow_advanced')}</button>
            </div>
          )}

          <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 8 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
              <span style={{ ...label, flex: 1 }}>{t('ui6ConnAuto.steps_count', { total: steps.length })}</span>
              <button className="btn btn-secondary btn-sm" onClick={() => runVerify(false)} disabled={busy || !draftDir} style={{ gap: 5 }}><ShieldCheck size={11} /> {t('ui6ConnAuto.verify_safe')}</button>
              <button className="btn btn-ghost btn-sm" onClick={() => runVerify(true)} disabled={busy || !draftDir} style={{ gap: 5 }}><Play size={11} /> {t('ui6ConnAuto.verify_full')}</button>
            </div>
            {missing.length > 0 && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                <span style={{ ...muted, fontSize: 10 }}>{t('ui6ConnAuto.missing_values')}</span>
                {missing.map(i => (
                  <div key={i.name} style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                    <span title={i.name} style={{ ...mono, fontSize: 10.5, color: 'var(--text)', width: 140, overflow: 'hidden', textOverflow: 'ellipsis' }}>{i.name}</span>
                    <input type={i.secret ? 'password' : 'text'} autoComplete="off" aria-label={t('ui6ConnAuto.value_for', { name: i.name })} value={verifyInputs[i.name] || ''}
                      onChange={e => setVerifyInputs(m => ({ ...m, [i.name]: e.target.value }))} style={{ ...inputStyle, flex: 1 }} />
                  </div>
                ))}
              </div>
            )}
            {phase === 'verifying' && <Busy text={t('ui6ConnAuto.replaying')} />}
            <ErrorBox>{verifyError}</ErrorBox>
            {(verify?.steps || []).some(scriptRefused) && (
              <div role="note" style={{ ...panel, borderColor: 'var(--yellow)', ...body, fontSize: 11 }}>
                {t('ui6ConnAuto.script_refused')}
              </div>
            )}
            {verify && (verify.ok
              ? <OkBox>{verify.stoppedAt ? t('ui6ConnAuto.verified_stopped') : t('ui6ConnAuto.all_passed')}{verify.healed?.length ? ` ${t('ui6ConnAuto.healed_promoted', { count: verify.healed.length })}` : ''}</OkBox>
              : <ErrorBox>{t('ui6ConnAuto.verify_failing')}</ErrorBox>)}
            {steps.length ? <StepList steps={steps} verify={verify} /> : <span style={muted}>{t('ui6ConnAuto.no_steps')}</span>}
          </div>

          {phase === 'saved' ? (
            <OkBox>
              {t('ui6ConnAuto.saved_word')} {saved?.nodeType ? t('ui6ConnAuto.saved_as_node', { node: saved.nodeType }) : ''}{saved?.version ? t('ui6ConnAuto.saved_version', { version: saved.version }) : ''}{saved?.workflowId ? t('ui6ConnAuto.saved_workflow', { id: saved.workflowId }) : ''}.
            </OkBox>
          ) : (
            <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
              <label htmlFor="rr-saveas" style={label}>{t('ui6ConnAuto.save_as')}</label>
              <select id="rr-saveas" className="form-select" value={saveAs} onChange={e => setSaveAs(e.target.value)} style={{ width: 'auto', padding: '5px 30px 5px 10px', fontSize: 11 }}>
                <option value="action">{t('ui6ConnAuto.as_action')}</option>
                <option value="fragment">{t('ui6ConnAuto.as_fragment')}</option>
                <option value="workflow">{t('ui6ConnAuto.as_workflow')}</option>
              </select>
              <button className="btn btn-primary btn-sm" onClick={() => save(false)} disabled={busy || !names.action || (draft.isNew && !names.automation)} style={{ gap: 5 }}>
                <Save size={11} /> {phase === 'saving' ? t('ui6ConnAuto.saving') : t('ui6ConnAuto.save')}
              </button>
              {!verify && <span style={{ ...muted, fontSize: 10 }}>{t('ui6ConnAuto.tip_verify')}</span>}
            </div>
          )}
          {forceAsk && phase !== 'saved' && (
            <div role="alert" style={{ ...panel, borderColor: 'var(--red)', display: 'flex', flexDirection: 'column', gap: 8 }}>
              <span style={{ ...label, color: 'var(--red)' }}>{t('ui6ConnAuto.draft_errors', { count: lintErrors.length })}</span>
              {lintErrors.map((i, n) => (
                <span key={n} style={{ ...mono, fontSize: 10.5, color: 'var(--text-secondary)' }}>{i.stepId ? `${i.stepId}: ` : ''}{i.message} <span style={{ color: 'var(--text-dim)' }}>({i.code})</span></span>
              ))}
              <span style={{ ...body, fontSize: 11 }}>{t('ui6ConnAuto.may_not_run')}</span>
              <div style={{ display: 'flex', gap: 8 }}>
                <button className="btn btn-danger btn-sm" onClick={() => save(true)} disabled={busy}>{t('ui6ConnAuto.save_anyway')}</button>
                <button className="btn btn-ghost btn-sm" onClick={() => setForceAsk(false)}>{t('ui6Conn.cancel')}</button>
              </div>
            </div>
          )}
          <ErrorBox>{saveError}</ErrorBox>
        </>
      )}
    </>
  )
}

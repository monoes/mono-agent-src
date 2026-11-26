// Actions tab: each action with its side effects, inputs and outputs, and
// Run test / Use in workflow / Export action.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import '../../i18n.js'
import { Play, Workflow, Download, FlaskConical, FileCode } from 'lucide-react'
import { api } from '../../services/api.js'
import { confirm } from '../../components/ConfirmDialog.jsx'
import { Chip, EffectChip, ErrorBox, OkBox, Busy, isRisky, body, label, mono, muted, panel, copyText } from './ui.jsx'


function Inputs({ inputs }) {
  const { t } = useTranslation()
  if (!inputs || !inputs.length) return <span style={muted}>{t('ui6ConnAuto.no_inputs')}</span>
  return (
    <table style={{ width: '100%', borderCollapse: 'collapse', tableLayout: 'fixed' }}>
      <colgroup><col style={{ width: '32%' }} /><col style={{ width: '14%' }} /><col /></colgroup>
      <tbody>
        {inputs.map(i => (
          <tr key={i.name} style={{ borderTop: '1px solid var(--border-dim)' }}>
            <td title={i.name} style={{ ...mono, fontSize: 10.5, color: 'var(--text)', padding: '4px 8px 4px 0', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', verticalAlign: 'top' }}>
              {i.name}{i.required && <span style={{ color: 'var(--red)' }}> *</span>}
            </td>
            <td style={{ ...mono, fontSize: 10, color: 'var(--cyan)', padding: '4px 8px 4px 0', verticalAlign: 'top' }}>{i.type || 'string'}</td>
            <td style={{ ...body, fontSize: 10.5, padding: '4px 0', verticalAlign: 'top' }}>
              {i.description}
              {i.default !== undefined && i.default !== null && i.default !== '' && <span style={{ ...muted, fontSize: 10 }}> {t('ui6ConnAuto.default_value', { value: JSON.stringify(i.default) })}</span>}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

// Outputs: the success outputs as chips, and any other outputs keys (the
// fields set when the action fails) listed apart and muted. outputsByKey is
// the action's outputs map as written; outputs is its flat union.
function Outputs({ a }) {
  const { t } = useTranslation()
  const byKey = a.outputsByKey
  const success = byKey ? (byKey.success || []) : (a.outputs || [])
  const other = byKey ? Object.entries(byKey).filter(([k, v]) => k !== 'success' && (v || []).length) : []
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
        <span style={{ ...label, marginRight: 4 }}>{t('ui6ConnAuto.outputs')}</span>
        {success.length ? success.map(o => <Chip key={o} color="var(--teal)">{o}</Chip>) : <span style={muted}>{t('ui6ConnAuto.none')}</span>}
      </div>
      {other.map(([k, v]) => (
        <span key={k} style={{ ...muted, fontSize: 10 }}>{t('ui6ConnAuto.on_key', { key: k, list: v.join(', ') })}</span>
      ))}
    </div>
  )
}

function TestResults({ res }) {
  const { t } = useTranslation()
  if (!res) return null
  if (res.error) return <ErrorBox>{res.error}</ErrorBox>
  const results = res.results || []
  if (!results.length) return <span style={muted}>{t('ui6ConnAuto.no_fixtures')}</span>
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
      {results.map((r, i) => (
        <div key={i} style={{ display: 'flex', gap: 6, alignItems: 'baseline' }}>
          <Chip color={r.ok ? 'var(--green-neon)' : 'var(--red)'}>{r.ok ? t('ui6ConnAuto.pass') : t('ui6ConnAuto.fail')}</Chip>
          <span style={{ ...mono, fontSize: 10, color: 'var(--text-secondary)' }}>{r.fixture ? t('ui6ConnAuto.fixture', { name: r.fixture }) : t('ui6ConnAuto.validation_lower')}{r.message ? ` — ${r.message}` : ''}</span>
        </div>
      ))}
    </div>
  )
}

function ActionRow({ automationId, a }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState('')
  const [testRes, setTestRes] = useState(null)
  const [note, setNote] = useState(null) // {ok, text}
  const nodeType = a.nodeType || `${automationId}.${a.name}`

  const runTest = async (live) => {
    if (live && isRisky(a.sideEffects) && !(await confirm(a.sideEffects
      ? t('ui6ConnAuto.run_live_effects', { name: a.name, effect: t(`ui6Conn.effect_value_${a.sideEffects}`, { defaultValue: a.sideEffects }) })
      : t('ui6ConnAuto.run_live_unknown', { name: a.name })))) return
    setBusy(live ? 'live' : 'test'); setTestRes(null)
    try { setTestRes(await api.testAutomation(automationId, a.name, live)) } finally { setBusy('') }
  }
  const useInWorkflow = async () => {
    await copyText(nodeType)
    setNote({ ok: true, text: t('ui6ConnAuto.copied_node', { node: nodeType }) })
  }
  const exportAction = async () => {
    const path = await api.chooseAutomationExportPath(`${nodeType}.mpkg`)
    if (!path) return
    setBusy('export')
    try {
      const res = await api.exportAction(nodeType, path)
      setNote(res?.error ? { ok: false, text: res.error } : { ok: true, text: t('ui6ConnAuto.exported_to', { path: res?.file || path }) })
    } finally { setBusy('') }
  }

  return (
    <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
        <span style={{ ...mono, fontSize: 12, fontWeight: 700, color: 'var(--text)' }}>{a.name}</span>
        <EffectChip effect={a.sideEffects} />
        {a.containsScript && <Chip color="var(--orange)"><FileCode size={8} /> {t('ui6ConnAuto.script')}</Chip>}
        <span style={{ ...muted, fontSize: 9.5, marginLeft: 'auto' }}>{nodeType}</span>
      </div>
      {a.description && <div style={{ ...body, fontSize: 11 }}>{a.description}</div>}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span style={label}>{t('ui6ConnAuto.inputs')}</span>
        <Inputs inputs={a.inputs} />
      </div>
      <Outputs a={a} />
      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        <button className="btn btn-secondary btn-sm" onClick={() => runTest(false)} disabled={!!busy} style={{ gap: 5 }}><FlaskConical size={11} /> {t('ui6ConnAuto.run_test')}</button>
        <button className="btn btn-ghost btn-sm" onClick={() => runTest(true)} disabled={!!busy} style={{ gap: 5 }}><Play size={11} /> {t('ui6ConnAuto.run_live')}</button>
        <button className="btn btn-ghost btn-sm" onClick={useInWorkflow} style={{ gap: 5 }}><Workflow size={11} /> {t('ui6ConnAuto.use_in_workflow')}</button>
        <button className="btn btn-ghost btn-sm" onClick={exportAction} disabled={!!busy} style={{ gap: 5 }}><Download size={11} /> {t('ui6ConnAuto.export_action')}</button>
      </div>
      {busy === 'test' && <Busy text={t('ui6ConnAuto.running_fixtures')} />}
      {busy === 'live' && <Busy text={t('ui6ConnAuto.running_live')} />}
      <TestResults res={testRes} />
      {note && (note.ok ? <OkBox>{note.text}</OkBox> : <ErrorBox>{note.text}</ErrorBox>)}
    </div>
  )
}

export default function ActionsTab({ automationId, actions }) {
  const { t } = useTranslation()
  if (!actions.length) return <div style={body}>{t('ui6ConnAuto.no_actions')}</div>
  return actions.map(a => <ActionRow key={a.name} automationId={automationId} a={a} />)
}

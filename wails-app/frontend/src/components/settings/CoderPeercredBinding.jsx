import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../services/api.js'

const mono = 'var(--font-mono)'
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const WARNINGS = ['coderBinding.warnMachine', 'coderBinding.warnTurns', 'coderBinding.warnLegacy', 'coderBinding.warnRun', 'coderBinding.warnAgent', 'coderBinding.warnOperator']

// CoderPeercredBinding is `coder set --peercred-binding`: whether a coder turn's tokens are bound to the turn's process.
// The open-ended chat mode is offered only while it is on. The CLI makes every check (this machine can run the binding
// service, no turn is running, no open-ended run is open when switching off), so a refusal is shown as the CLI words it.
// status is the coder status; onSaved gets the status the CLI returns.
export default function CoderPeercredBinding({ status, onSaved }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [saved, setSaved] = useState(false)
  const on = !!status?.peercredBinding

  const change = async (e) => {
    setBusy(true); setErr(''); setSaved(false)
    try { onSaved(await api.coderSetPeercredBinding(e.target.checked)); setSaved(true) } catch (x) { setErr(String(x?.message || x)) } finally { setBusy(false) }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }} data-testid="coder-peercred-binding">
      <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontFamily: mono, fontSize: 11, color: 'var(--text-secondary)' }}>
        <input type="checkbox" checked={on} disabled={busy} onChange={change} aria-label={t('coderBinding.label')} style={{ accentColor: '#00b4d8' }} />
        {t('coderBinding.label')}
      </label>
      <span style={hint}>{t('coderBinding.hint')}</span>
      <div data-testid="coder-binding-warning" style={{ ...hint, color: 'var(--yellow)' }}>
        <strong>{t('coderBinding.warnTitle')}</strong>
        <ul style={{ margin: '2px 0 0', paddingLeft: 18 }}>
          {WARNINGS.map(k => <li key={k}>{t(k)}</li>)}
        </ul>
      </div>
      {saved && <span role="status" style={{ fontFamily: mono, fontSize: 10.5, color: 'var(--green-neon)' }}>{t(on ? 'coderBinding.switchedOn' : 'coderBinding.switchedOff')}</span>}
      {err && <span role="alert" style={{ fontFamily: mono, fontSize: 10.5, color: 'var(--red)' }}>{err}</span>}
    </div>
  )
}

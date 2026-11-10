import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../services/api.js'

const mono = 'var(--font-mono)'
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const RISKS = ['cannotRun', 'noTurn', 'legacy', 'offRun', 'unattended']

// CoderPeercredBinding is `coder set --peercred-binding`: whether a turn's tokens are bound to the process that runs
// the turn. The open-ended mode is offered only while it is on. status is the coder status; onSaved gets the status the
// CLI returns. The CLI holds every check (machine, running turns, an open run) and its refusal is shown as it is said.
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
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }} data-testid="coder-peercred">
      <span style={label}>{t('coderPeercred.label')}</span>
      <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
        <input type="checkbox" role="switch" checked={on} onChange={change} disabled={busy} aria-label={t('coderPeercred.label')} />
        {on ? t('coderPeercred.on') : t('coderPeercred.off')}
      </label>
      <span style={hint}>{t('coderPeercred.hint')}</span>
      <div data-testid="coder-peercred-risks" style={{ ...hint, color: 'var(--yellow)' }}>
        <strong>{t('coderPeercred.riskTitle')}</strong>
        <ul style={{ margin: '2px 0 0', paddingLeft: 16 }}>
          {RISKS.map(k => <li key={k}>{t(`coderPeercred.risk.${k}`)}</li>)}
        </ul>
      </div>
      {saved && <span role="status" style={{ fontFamily: mono, fontSize: 10.5, color: 'var(--green-neon)' }}>{t('coderPeercred.saved')}</span>}
      {err && <span role="alert" style={{ fontFamily: mono, fontSize: 10.5, color: 'var(--red)' }}>{err}</span>}
    </div>
  )
}

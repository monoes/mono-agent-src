import { useTranslation } from 'react-i18next'
import { useAgentPurposes } from './useAgentPurposes.js'
import PurposeCard from './PurposeCard.jsx'

// Settings > Agent purposes (monoes/mono-agent-src#1150): for each kind of
// work, which validated (system, model) pairs agents may use. The CLI
// (`monoagentcli agent purposes`) owns the purposes, the rules and the roster;
// this only renders them and shells out, through app_agent_purposes.go.

const mono = 'var(--font-mono)'
const card = {
  background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)',
  padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 10, marginBottom: 28,
}
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }

export default function AgentPurposesSection() {
  const { t } = useTranslation()
  const { doc, loadErr, load, pending, errors, save, reset, clearError } = useAgentPurposes()

  return (
    <div data-testid="agent-purposes" style={card}>
      <div style={hint}>{t('settings.purposes.intro')}</div>
      {!doc && !loadErr && <div style={hint} role="status">{t('settings.purposes.loading')}</div>}
      {loadErr && (
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <span style={errText} role="alert">{t('settings.purposes.loadError', { message: loadErr })}</span>
          <button type="button" className="btn btn-ghost btn-sm" onClick={load}>{t('settings.purposes.retry')}</button>
        </div>
      )}
      {doc && doc.roster.length === 0 && <div style={hint} data-testid="purposes-empty-roster">{t('settings.purposes.emptyRoster')}</div>}
      {doc && (
        <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column' }}>
          {doc.purposes.map(p => (
            <PurposeCard key={p.id} purpose={p} roster={doc.roster} pending={!!pending[p.id]} error={errors[p.id]}
              onSave={save} onReset={reset} onDismissError={clearError} />
          ))}
        </ul>
      )}
    </div>
  )
}

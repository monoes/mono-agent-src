import { useTranslation } from 'react-i18next'
import { Loader } from 'lucide-react'
import { api } from '../../services/api.js'
import { ACCESS_LEVELS, isCoderAccess } from '../../lib/chatAccess.js'
import { runtimeLabel } from '../../lib/runtimeLabels.js'
import AgentSetupLink from '../AgentSetupLink.jsx'
import { FolderPicker } from '../chat/FolderPicker.jsx'
import FixMonomindButton from '../FixMonomindButton.jsx'
import { useRecentWorkspaces, coderReady, fidelityNote, missingText, monomindNeedsFix } from '../chat/useCoderMode.js'

const mono = 'var(--font-mono)'
// Box style for the loading placeholder in the selector row. The <select>s
// themselves use the global `select` rule plus `.select-compact` (index.css).
const placeholderStyle = {
  background: '#020509', border: '1px solid rgba(0,180,216,0.15)', borderRadius: 6,
  padding: '4px 8px', color: '#e2e8f0', fontFamily: mono, fontSize: 10, minWidth: 0,
}
const note = { padding: '0 12px 8px', fontFamily: mono, fontSize: 9.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const warn = { padding: '8px 12px', fontFamily: mono, fontSize: 10.5, color: '#fbbf24', lineHeight: 1.6 }

// whyBlocked is the setup state in words, with the link that resolves it.
// Used for the banner here and for the composer's disabled reason.
export function BlockedReason({ setup: s, onNavigate, compact = false }) {
  const { t } = useTranslation()
  if (s.state === 'ready') return null
  if (s.state === 'loading') return <>{t('bubbles.pane.loadingSystems')}</>
  const link = <AgentSetupLink onNavigate={onNavigate} compact={compact} />
  if (s.state === 'scan-missing') return <>{t('bubbles.pane.monomindMissing')} <code>npm install -g @monoes/monomindcli</code> {link}</>
  if (s.state === 'scan-error') return <>{t('bubbles.pane.monomindError')} <code>{s.scanError}</code> {link}</>
  if (s.state === 'no-models') return <>{t('bubbles.pane.noModels')} {link}</>
  return <>{t('agentSetup.noRuntime')} {link}</>
}

// ChatSetup is a new chat's choices before its first message (see
// useChatSetup): access level, folder (above scoped), runtime, model and
// effort. setup is the bubble's draft setup, s the hook's result.
export function ChatSetup({ s, setup, onChange, onNavigate }) {
  const { t } = useTranslation()
  const recent = useRecentWorkspaces(s.coder && !!s.status?.enabled)
  if (!s.settled) {
    return <div style={{ padding: 16, fontFamily: mono, fontSize: 10, color: 'var(--text-muted)' }}>{t('bubbles.loading')}</div>
  }
  const coderOff = !s.status?.enabled
  const coderBlocked = coderOff || !s.coderOk
  const pickFolder = async () => {
    const dir = await Promise.resolve(api.pickCoderFolder()).catch(() => '')
    if (dir) onChange({ workspace: { kind: 'folder', path: dir } })
  }
  const setAccess = access => onChange({ access, runtime: '', model: '', effort: '' })
  const fidelity = fidelityNote(s.info)

  return (
    <div data-testid="chat-setup">
      <div style={{ display: 'flex', gap: 6, alignItems: 'center', padding: '8px 12px 0' }}>
        <label htmlFor="setup-access" style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-muted)' }}>{t('bubbles.pane.access')}</label>
        <select id="setup-access" value={s.access} onChange={e => setAccess(e.target.value)} className="select-compact" style={{ minWidth: 120 }}>
          {ACCESS_LEVELS.map(l => <option key={l} value={l} disabled={isCoderAccess(l) && coderBlocked}>{t(`bubbles.pane.level.${l}`)}</option>)}
        </select>
      </div>
      <div style={note}>{t(`bubbles.pane.allows.${s.access}`)}</div>
      {coderOff && (
        <div data-testid="coder-disabled" style={warn}>
          {t('bubbles.pane.coderOffScoped')}{' '}
          <button type="button" onClick={() => onNavigate?.('settings')} className="btn btn-ghost btn-sm" style={{ fontSize: 10 }}>{t('bubbles.openSettings')}</button>
        </div>
      )}
      {!coderOff && !s.coderOk && (
        <div style={warn}>
          {t('bubbles.coderNotReady', { why: missingText(s.status) })}
          {monomindNeedsFix(s.status) && <> <FixMonomindButton onDone={s.refreshStatus} /></>}
        </div>
      )}
      {s.coder && coderReady(s.status) && (
        <div style={{ padding: '8px 12px', borderBottom: '1px solid rgba(0,180,216,0.06)' }}>
          <FolderPicker status={s.status} workspace={setup.workspace} onWorkspaceChange={workspace => onChange({ workspace })}
            recent={recent} onPickFolder={pickFolder} runtimeId={s.runtime} />
        </div>
      )}
      {s.options.length > 0 && (
        <div style={{ display: 'flex', gap: 6, padding: '8px 12px', borderBottom: '1px solid rgba(0,180,216,0.06)' }}>
          <select aria-label={t('bubbles.runtime')} value={s.runtime} onChange={e => onChange({ runtime: e.target.value, model: '', effort: '' })} className="select-compact" style={{ minWidth: 90 }}>
            {s.options.map(r => <option key={r.id} value={r.id}>{runtimeLabel(r.id)}</option>)}
          </select>
          {s.models === null ? (
            <span style={{ ...placeholderStyle, flex: 1, display: 'flex', alignItems: 'center', gap: 6 }}>
              <Loader size={11} className="chat-spin" /> {t('bubbles.loadingModels')}
            </span>
          ) : (
            <select aria-label={t('bubbles.model')} value={setup.model} onChange={e => onChange({ model: e.target.value, effort: '' })} className="select-compact" style={{ flex: 1, minWidth: 0 }}>
              {s.models.map(m => <option key={m.id} value={m.id}>{m.label || m.id}</option>)}
            </select>
          )}
          {s.efforts.length > 0 && (
            <select aria-label={t('bubbles.effort')} value={setup.effort} onChange={e => onChange({ effort: e.target.value })} className="select-compact" style={{ minWidth: 72 }}>
              <option value="">{t('bubbles.effortAuto')}</option>
              {s.efforts.map(eff => <option key={eff} value={eff}>{eff}</option>)}
            </select>
          )}
        </div>
      )}
      {fidelity && <div style={note}>{fidelity}</div>}
      {s.state !== 'ready' && s.state !== 'loading' && (
        <div data-testid="setup-blocked" style={warn}>
          <BlockedReason setup={s} onNavigate={onNavigate} />
          {(s.state === 'scan-error' || s.state === 'scan-missing') && (
            <> <button type="button" className="btn btn-ghost btn-sm" style={{ fontSize: 10 }} onClick={s.retryScan}>{t('bubbles.pane.retryScan')}</button></>
          )}
        </div>
      )}
    </div>
  )
}

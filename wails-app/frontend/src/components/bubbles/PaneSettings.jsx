import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Settings2 } from 'lucide-react'
import { api } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'
import { chatAccessApi } from '../../lib/chatAccessApi.js'
import { ACCESS_LEVELS, isAccess, isCoderAccess, isRaise, browserKindFor } from '../../lib/chatAccess.js'
import { getAssistantTools, getAssistantAllowRuns } from '../../lib/assistantTools.js'
import { runtimeLabel } from '../../lib/runtimeLabels.js'
import { money } from '../settings/aitaskBudgetApi.js'
import { CapLower } from './PaneCapLower.jsx'
import { SessionSettings } from './SessionSettings.jsx'
import { useBrowserLevel, useCostVsCap } from './useChatPaneData.js'

const mono = 'var(--font-mono)'
const row = { display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', padding: '6px 0', borderBottom: '1px solid var(--border)' }
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', minWidth: 78 }
const value = { fontFamily: mono, fontSize: 11, color: 'var(--text)' }

// SettingsButton opens the pane's settings popover; the overlay owns the open
// state (its Escape handling closes the popover first).
export const SettingsButton = ({ open, onToggle, buttonRef }) => {
  const { t } = useTranslation()
  return (
    <button ref={buttonRef} type="button" className="btn btn-ghost btn-sm" onClick={onToggle} aria-haspopup="dialog" aria-expanded={open}
      title={t('bubbles.pane.settings')} aria-label={t('bubbles.pane.settings')} style={{ padding: '3px 6px' }}>
      <Settings2 size={13} />
    </button>
  )
}

// AccessRow shows the level and changes it. Lowering is immediate and reports
// what it could not stop; raising asks first, stating what the level allows.
function AccessRow({ bubble, access: level, store, confined }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [survivors, setSurvivors] = useState([])
  const [done, setDone] = useState('')

  // The level compared against is the one the backend confirmed, never the
  // cached one; the backend refuses the change if it moved meanwhile.
  const change = async (next) => {
    if (busy || !isAccess(level) || next === level) return
    setError(''); setSurvivors([]); setDone('')
    const raise = isRaise(level, next)
    if (raise) {
      const ok = await confirm(t('bubbles.pane.raiseBody', { level: t(`bubbles.pane.level.${next}`), allows: t(`bubbles.pane.allows.${next}`) }), {
        title: t('bubbles.pane.raiseTitle', { level: t(`bubbles.pane.level.${next}`) }), confirmLabel: t('bubbles.pane.raiseConfirm'), cancelLabel: t('bubbles.pane.cancel'), danger: true,
      })
      if (!ok) return
    }
    setBusy(true)
    try {
      const res = await chatAccessApi.setAccess(bubble.conversationId, next, level, raise)
      if (res?.ok === false) {
        // Refused: the level was not what this pane thought. Show the real one; nothing changed.
        if (isAccess(res.current)) store.patchBubble(bubble.key, { access: res.current, verified: true })
        setError(res.code === 'access_changed'
          ? t('bubbles.pane.accessChanged', { level: isAccess(res.current) ? t(`bubbles.pane.level.${res.current}`) : t('bubbles.pane.accessUnknown') })
          : t('bubbles.pane.accessRefused'))
        return
      }
      const conv = res?.conversation || {}
      store.patchBubble(bubble.key, { access: isAccess(conv.access) ? conv.access : next, verified: true, cwd: conv.cwd || bubble.cwd })
      const left = res?.lowered?.survivors || []
      setSurvivors(left)
      setDone(left.length ? '' : t('bubbles.pane.accessNow', { level: t(`bubbles.pane.level.${next}`) }))
    } catch (err) {
      setError(err?.code === 'operator_only' ? t('bubbles.pane.operatorOnly') : String(err?.message || err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ ...row, alignItems: 'flex-start' }}>
      <label htmlFor="pane-access" style={label}>{t('bubbles.pane.access')}</label>
      {confined ? (
        <span style={value} data-testid="pane-access-confined">{t('bubbles.pane.confined')}</span>
      ) : (
        <select id="pane-access" value={isAccess(level) ? level : ''} disabled={busy || !isAccess(level)} onChange={e => change(e.target.value)} className="select-compact" style={{ minWidth: 120 }}>
          {!isAccess(level) && <option value="">{t('bubbles.pane.accessUnknown')}</option>}
          {ACCESS_LEVELS.map(l => <option key={l} value={l}>{t(`bubbles.pane.level.${l}`)}</option>)}
        </select>
      )}
      {isAccess(level) && !confined && <span style={{ ...label, flexBasis: '100%', minWidth: 0 }}>{t(`bubbles.pane.allows.${level}`)}</span>}
      {!isAccess(level) && !confined && <span style={{ ...label, flexBasis: '100%', minWidth: 0 }}>{t('bubbles.pane.accessUnknownWhy')}</span>}
      <div role="status" aria-live="polite" style={{ flexBasis: '100%', fontFamily: mono, fontSize: 10, color: 'var(--text-muted)' }}>{done}</div>
      {survivors.length > 0 && (
        <div role="alert" data-testid="pane-survivors" style={{ flexBasis: '100%', fontFamily: mono, fontSize: 10, color: '#fbbf24' }}>
          <div>{t('bubbles.pane.survivors', { count: survivors.length })}</div>
          <ul style={{ margin: '4px 0 0', paddingLeft: 16 }}>
            {survivors.map(s => <li key={s.pid}>{s.pid} {s.command} {s.reason ? `(${s.reason})` : ''}</li>)}
          </ul>
        </div>
      )}
      {error && <div role="alert" style={{ flexBasis: '100%', fontFamily: mono, fontSize: 10, color: 'var(--red)' }}>{error}</div>}
    </div>
  )
}

function OrgRow({ bubble, store }) {
  const { t } = useTranslation()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const on = bubble.orgMode === 'dynamic'
  const toggle = async () => {
    setBusy(true); setError('')
    try {
      await api.setChatOrgMode(bubble.conversationId, on ? 'solo' : 'dynamic')
      store.patchBubble(bubble.key, { orgMode: on ? '' : 'dynamic' })
    } catch (err) {
      setError(String(err?.message || err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div style={row}>
      <span id="pane-org-label" style={label}>{t('bubbles.pane.orgMode')}</span>
      <button type="button" role="switch" aria-checked={on} aria-labelledby="pane-org-label" disabled={busy} onClick={toggle} className="btn btn-ghost btn-sm" style={{ fontSize: 10 }}>
        {on ? t('bubbles.pane.orgOn') : t('bubbles.pane.orgOff')}
      </button>
      {error && <span role="alert" style={{ color: 'var(--red)', fontFamily: mono, fontSize: 10 }}>{error}</span>}
    </div>
  )
}

const linkBtn = { fontSize: 10, padding: '1px 6px' }

// PaneSettings is the popover of one open conversation: what it runs on
// (fixed once created), its access level, its spend against the cap, the
// browser level for its kind, the org mode and the assistant tools. Only the
// access level and the org mode are changed here; the rest links to Settings.
export function PaneSettings({ bubble, access, store, confined, onNavigate, onClose }) {
  const { t } = useTranslation()
  const ref = useRef(null)
  const coder = isCoderAccess(access)
  const [costVersion, setCostVersion] = useState(0)
  const cost = useCostVsCap(bubble.taskId || '', true, costVersion)
  // A confined apply chat runs under the apply browser kind.
  const browserKind = confined ? 'apply' : browserKindFor(access)
  const browser = useBrowserLevel(browserKind, true)

  // Focus lands in the popover when it opens.
  useEffect(() => { ref.current?.querySelector('select, button')?.focus?.() }, [])

  const toSettings = () => { onClose(); onNavigate?.('settings') }
  const tools = getAssistantTools()
  const runs = getAssistantAllowRuns()

  return (
    <div ref={ref} role="dialog" aria-label={t('bubbles.pane.settings')} data-testid="pane-settings" className="pane-settings">
      <div style={row}>
        <span style={label}>{t('bubbles.pane.runs')}</span>
        <span style={value}>{[bubble.runtime && runtimeLabel(bubble.runtime), bubble.model, bubble.effort].filter(Boolean).join(' · ') || t('bubbles.pane.none')}</span>
        <span style={{ ...label, flexBasis: '100%', minWidth: 0 }}>{t('bubbles.pane.fixed')}</span>
      </div>
      <AccessRow bubble={bubble} access={access} store={store} confined={confined} />
      <div style={row} data-testid="pane-cost">
        <span style={label}>{t('bubbles.pane.spend')}</span>
        <span style={value}>
          {cost === undefined ? t('bubbles.loading')
            : cost ? t(cost.scope === 'task' ? 'bubbles.pane.spendTask' : 'bubbles.pane.spendGlobal', { spent: cost.spentUsd == null ? '–' : money(cost.spentUsd), cap: money(cost.capUsd) })
              : t('bubbles.pane.noCap')}
        </span>
        <button type="button" className="btn btn-ghost btn-sm" style={linkBtn} onClick={toSettings}>{t('bubbles.pane.budgetsLink')}</button>
        <CapLower taskId={bubble.taskId || ''} conversationId={bubble.conversationId || ''} cost={cost} enabled={isAccess(access)} confined={confined} onLowered={() => setCostVersion(v => v + 1)} />
      </div>
      <div style={row} data-testid="pane-browser">
        <span style={label}>{t('bubbles.pane.browser')}</span>
        <span style={value}>{browser === undefined ? t('bubbles.loading') : browser ? browser.label : t('bubbles.pane.unknown')}</span>
        <button type="button" className="btn btn-ghost btn-sm" style={linkBtn} onClick={toSettings}>{t('bubbles.pane.browserLink', { kind: browserKind })}</button>
      </div>
      {coder && !confined && <OrgRow bubble={bubble} store={store} />}
      {coder && !confined && bubble.conversationId && <SessionSettings conversationId={bubble.conversationId} access={access} />}
      {access === 'scoped' && (
        <div style={row} data-testid="pane-tools">
          <span style={label}>{t('bubbles.pane.tools')}</span>
          <span style={value}>{t('bubbles.pane.toolsState', { tools: tools ? t('bubbles.pane.on') : t('bubbles.pane.off'), runs: runs ? t('bubbles.pane.on') : t('bubbles.pane.off') })}</span>
          <button type="button" className="btn btn-ghost btn-sm" style={linkBtn} onClick={toSettings}>{t('bubbles.pane.toolsLink')}</button>
        </div>
      )}
    </div>
  )
}

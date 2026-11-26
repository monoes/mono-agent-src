import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, ChevronDown, Wrench, Copy, Loader2, X } from 'lucide-react'

// One row of Settings › System health: a doctor check (or one of its child
// rows), its fix button and, while a fix runs, its live output.

export const STATUS_STYLE = {
  ok: { mark: '✓', color: 'var(--green-neon)' },
  warn: { mark: '⚠', color: '#fbbf24' },
  fail: { mark: '✗', color: 'var(--red)' },
  info: { mark: '•', color: 'var(--text-muted)' },
  skip: { mark: '–', color: 'var(--text-muted)' },
}

const mono = { fontFamily: 'var(--font-mono)' }

function hasProblem(row) {
  return row.status === 'warn' || row.status === 'fail' || (row.children || []).some(hasProblem)
}

// quiet: a row action (not a fix) — no "optional" tag, lighter styling.
// onCancel: while the fix runs, a Cancel button next to it stops it.
export function FixButton({ fix, state, onFix, quiet, onCancel }) {
  const { t } = useTranslation()
  const running = state?.running
  const manual = fix.safety === 'manual'
  const Icon = running ? Loader2 : manual ? Copy : Wrench
  return (
    <>
    <button
      onClick={() => onFix(fix)}
      disabled={running}
      title={fix.command || fix.label}
      style={{
        ...mono, fontSize: 10, display: 'inline-flex', alignItems: 'center', gap: 5,
        padding: '3px 10px', borderRadius: 4, cursor: running ? 'default' : 'pointer', flexShrink: 0,
        background: manual || quiet ? 'transparent' : 'rgba(0,180,216,.15)',
        color: manual || quiet ? 'var(--text-secondary)' : '#00b4d8',
        border: manual || quiet ? '1px solid var(--border)' : '1px solid rgba(0,180,216,.25)',
        opacity: running ? 0.6 : 1,
      }}
    >
      <Icon size={11} className={running ? 'spin' : undefined} />
      {running ? t('settings.health.fixing') : manual ? t('settings.health.copyCommand') : fix.label}
      {fix.optional && !running && !quiet && (
        <span style={{ color: 'var(--text-muted)', fontSize: 9 }}>· {t('settings.health.optional')}</span>
      )}
    </button>
    {running && onCancel && (
      <button
        onClick={() => onCancel(fix.id)}
        aria-label={`${t('settings.health.cancel')}: ${fix.label}`}
        style={{ ...mono, fontSize: 10, display: 'inline-flex', alignItems: 'center', gap: 4, padding: '3px 8px', borderRadius: 4, cursor: 'pointer', flexShrink: 0, background: 'transparent', color: 'var(--text-secondary)', border: '1px solid var(--border)' }}
      >
        <X size={11} />{t('settings.health.cancel')}
      </button>
    )}
    </>
  )
}

export default function HealthRow({ row, depth = 0, fixStates, onFix, onCancel }) {
  const { t } = useTranslation()
  const children = row.children || []
  const [open, setOpen] = useState(() => children.some(hasProblem))
  const [details, setDetails] = useState(false)
  const st = STATUS_STYLE[row.status] || STATUS_STYLE.info
  const actions = row.actions || []
  // The live output shown under the row: its fix's, or the latest action's.
  const fixState = [row.fix, ...actions].filter(Boolean).map(f => fixStates[f.id]).filter(Boolean).pop() || null
  const problem = row.status === 'warn' || row.status === 'fail'
  const hasDetails = !!row.detail || (problem && row.features?.length > 0) || (row.fix?.safety === 'manual' && row.fix.command)

  return (
    <div>
      <div
        data-health-row={row.id}
        style={{
          display: 'flex', alignItems: 'center', gap: 10, padding: '7px 0',
          paddingLeft: depth * 18, borderTop: depth === 0 ? '1px solid var(--border)' : 'none',
        }}
      >
        <span role="img" aria-label={STATUS_STYLE[row.status] ? t(`ui6SetHealth.status_${row.status}`) : row.status} style={{ ...mono, width: 14, textAlign: 'center', color: st.color, flexShrink: 0 }}>{st.mark}</span>
        {children.length > 0 ? (
          <button
            onClick={() => setOpen(o => !o)}
            aria-expanded={open}
            aria-label={open ? t('settings.health.hideItems') : t('settings.health.showItems', { n: children.length })}
            style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', color: 'var(--text-muted)', display: 'flex' }}
          >
            {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
          </button>
        ) : null}
        <span style={{ ...mono, fontSize: 11.5, color: 'var(--text)', minWidth: 150, flexShrink: 0 }}>
          {row.title}
          {row.required && row.status === 'fail' && (
            <span style={{ color: 'var(--red)', fontSize: 9, marginLeft: 6 }}>{t('settings.health.required')}</span>
          )}
          {row.carried && depth === 0 && (
            // Kept from an earlier (deeper, or runtime-scanning) check that
            // the latest check didn't repeat.
            <span title={row.checked_at ? new Date(row.checked_at).toLocaleString() : undefined} style={{ color: 'var(--text-muted)', fontSize: 9, marginLeft: 6 }}>
              {t('settings.health.earlierCheck')}
            </span>
          )}
        </span>
        {hasDetails ? (
          // A button, so the details (and a manual fix's instructions) can
          // be opened from the keyboard.
          <button
            type="button"
            aria-expanded={details}
            onClick={() => setDetails(d => !d)}
            title={row.summary}
            style={{ ...mono, fontSize: 11, color: 'var(--text-secondary)', flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', cursor: 'pointer', background: 'none', border: 'none', padding: 0, textAlign: 'left' }}
          >
            {row.summary}
            <span aria-hidden="true" style={{ color: 'var(--text-muted)', marginLeft: 6 }}>{details ? '▾' : '▸'}</span>
          </button>
        ) : (
          <span
            style={{ ...mono, fontSize: 11, color: 'var(--text-secondary)', flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
            title={row.summary}
          >
            {row.summary}
          </span>
        )}
        {row.fix && <FixButton fix={row.fix} state={fixStates[row.fix.id]} onFix={onFix} onCancel={onCancel} />}
        {actions.map(a => <FixButton key={a.id} fix={a} state={fixStates[a.id]} onFix={onFix} onCancel={onCancel} quiet />)}
      </div>

      {details && hasDetails && (
        <div style={{ ...mono, fontSize: 10.5, color: 'var(--text-muted)', paddingLeft: depth * 18 + 24, paddingBottom: 8, whiteSpace: 'pre-wrap', wordBreak: 'break-word', lineHeight: 1.55 }}>
          {row.detail}
          {problem && row.features?.length > 0 && (
            <div>{t('settings.health.affects')}: {row.features.join(', ')}</div>
          )}
          {row.fix?.safety === 'manual' && row.fix.command && (
            <div style={{ color: 'var(--text-secondary)' }}>{t('settings.health.toFix')}: {row.fix.command}</div>
          )}
        </div>
      )}

      {fixState && (fixState.running || fixState.lines?.length > 0 || fixState.error || fixState.cancelled) && (
        <div role="status" aria-live="polite" style={{ ...mono, fontSize: 10, color: 'var(--text-muted)', margin: `0 0 8px ${depth * 18 + 24}px`, padding: '6px 10px', background: 'rgba(0,0,0,.25)', borderRadius: 4, maxHeight: 130, overflowY: 'auto', whiteSpace: 'pre-wrap' }}>
          {(fixState.lines || []).map((l, i) => <div key={i}>{l}</div>)}
          {fixState.error && <div style={{ color: 'var(--red)' }}>{fixState.error}</div>}
          {fixState.done && !fixState.error && <div style={{ color: 'var(--green-neon)' }}>✓ {t('settings.health.fixDone')}</div>}
          {fixState.cancelled && <div>{t('settings.health.fixCancelled')}</div>}
        </div>
      )}

      {open && children.map(c => (
        <HealthRow key={c.id} row={c} depth={depth + 1} fixStates={fixStates} onFix={onFix} onCancel={onCancel} />
      ))}
    </div>
  )
}

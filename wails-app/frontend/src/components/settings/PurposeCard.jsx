import { useState, useId } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, Loader2 } from 'lucide-react'
import { confirm } from '../ConfirmDialog.jsx'
import { Switch } from './jevShared.jsx'
import PairChips from './PairChips.jsx'
import {
  RULES, FALLBACKS, draftFrom, isDirty, budgetError, effortOptions, groupRoster, pairKey, signature, rosterStatus,
} from '../../lib/agentPurposes.js'

// One purpose: a header row that opens an editor. The editor keeps a draft and
// saves it whole with one Save (the CLI's `set` replaces the purpose), so a
// half-edited rule never reaches the resolver. Reset goes back to "every
// validated model" after a confirm.

const mono = 'var(--font-mono)'
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }
const fieldLabel = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
const field = { display: 'flex', flexDirection: 'column', gap: 4, flex: '1 1 180px', minWidth: 0 }

export default function PurposeCard({ purpose, roster, pending, error, onSave, onReset, onDismissError }) {
  const { t } = useTranslation()
  const uid = useId()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState(() => draftFrom(purpose))
  const saved = signature(draftFrom(purpose))
  // The CLI's copy is the truth: when it changes (after a save or a reset) the draft follows it.
  const [base, setBase] = useState(saved)
  if (base !== saved) { setBase(saved); setDraft(draftFrom(purpose)) }

  const dirty = isDirty(draft, purpose)
  const bErr = budgetError(draft.budgetShare)
  const set = (patch) => { if (error) onDismissError(purpose.id); setDraft(d => ({ ...d, ...patch })) }
  const chosen = new Set(draft.pairs.map(pairKey))
  const name = t(`settings.purposes.names.${purpose.id}`, { defaultValue: purpose.title })
  const desc = t(`settings.purposes.descriptions.${purpose.id}`, { defaultValue: purpose.description })

  const summary = purpose.pairs.length === 0
    ? t('settings.purposes.usesEvery')
    : t(purpose.pairs.length === 1 ? 'settings.purposes.summaryOne' : 'settings.purposes.summaryMany', { count: purpose.pairs.length })

  const unusable = purpose.pairs.filter(x => rosterStatus(roster, x) !== 'ok').length

  const doReset = async () => {
    const ok = await confirm(t('settings.purposes.resetBody', { name }), {
      title: t('settings.purposes.resetTitle'), confirmLabel: t('settings.purposes.reset'), cancelLabel: t('settings.purposes.cancel'), danger: true,
    })
    if (ok) await onReset(purpose.id)
  }

  const addPair = (key) => {
    if (!key) return
    const hit = roster.find(r => pairKey(r) === key)
    if (hit && hit.status === 'ok' && !chosen.has(key)) set({ pairs: [...draft.pairs, { runtime: hit.runtime, model: hit.model }] })
  }

  const panelId = `${uid}-panel`
  const id = (s) => `${uid}-${s}`
  const retryable = error && error.code !== 'operator_only'

  return (
    <li data-purpose={purpose.id} data-default={purpose.isDefault ? 'true' : 'false'}
      style={{ borderTop: '1px solid var(--border)', padding: '2px 0' }}>
      <button type="button" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen(o => !o)}
        style={{
          display: 'flex', alignItems: 'center', gap: 8, width: '100%', background: 'none', border: 0, cursor: 'pointer',
          padding: '8px 0', textAlign: 'left', color: 'var(--text)', flexWrap: 'wrap',
        }}>
        {open ? <ChevronDown size={12} aria-hidden="true" /> : <ChevronRight size={12} aria-hidden="true" />}
        <span style={{ fontFamily: 'var(--font-body)', fontSize: 12.5, flex: '1 1 140px', minWidth: 0 }}>{name}</span>
        {dirty && <span style={{ ...hint, color: 'var(--yellow)' }}>{t('settings.purposes.unsaved')}</span>}
        <span data-testid="purpose-summary" style={{ fontFamily: mono, fontSize: 10.5, color: purpose.isDefault ? 'var(--text-muted)' : 'var(--cyan)' }}>
          {summary}
          {unusable > 0 && <span style={{ color: 'var(--yellow)' }}>{` · ${t('settings.purposes.summaryNotUsable', { count: unusable })}`}</span>}
        </span>
      </button>

      {open && (
        <div id={panelId} role="group" aria-label={name} style={{ display: 'flex', flexDirection: 'column', gap: 12, padding: '4px 0 14px 20px' }}>
          <div style={hint}>{desc}</div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={fieldLabel} id={id('models')}>{t('settings.purposes.models')}</span>
            <PairChips pairs={draft.pairs} roster={roster} ordered={draft.rule === 'order'} disabled={pending}
              labelledBy={id('models')} onChange={(pairs) => set({ pairs })} />
            {draft.pairs.some(x => rosterStatus(roster, x) !== 'ok') && (
              <div style={{ ...hint, color: 'var(--yellow)' }}>{t('settings.purposes.notUsableHint')}</div>
            )}
            <AddModel idBase={id('add')} roster={roster} chosen={chosen} disabled={pending} onPick={addPair} />
            {draft.pairs.length === 0 && <div style={hint}>{t('settings.purposes.nothingChosen')}</div>}
          </div>

          <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            <label style={field}>
              <span style={fieldLabel}>{t('settings.purposes.rule')}</span>
              <select className="form-select" value={draft.rule} disabled={pending} onChange={e => set({ rule: e.target.value })}>
                {RULES.map(r => <option key={r} value={r}>{t(`settings.purposes.rules.${r}`)}</option>)}
              </select>
            </label>
            <label style={field}>
              <span style={fieldLabel}>{t('settings.purposes.effort')}</span>
              <select className="form-select" value={draft.effort} disabled={pending} onChange={e => set({ effort: e.target.value })}>
                <option value="">{t('settings.purposes.effortDefault')}</option>
                {effortOptions(draft.effort).map(x => <option key={x} value={x}>{x}</option>)}
              </select>
            </label>
            <label style={field}>
              <span style={fieldLabel}>{t('settings.purposes.fallback')}</span>
              <select className="form-select" value={draft.fallback} disabled={pending} onChange={e => set({ fallback: e.target.value })}>
                {FALLBACKS.map(f => <option key={f} value={f}>{t(`settings.purposes.fallbacks.${f}`)}</option>)}
              </select>
            </label>
            <div style={{ ...field, flex: '0 1 130px' }}>
              <label htmlFor={id('share')} style={fieldLabel}>{t('settings.purposes.budgetShare')}</label>
              <input id={id('share')} className="form-input" inputMode="decimal" placeholder="%" value={draft.budgetShare}
                disabled={pending} aria-invalid={!!bErr} aria-describedby={bErr ? id('share-err') : undefined}
                onChange={e => set({ budgetShare: e.target.value })} style={{ fontFamily: mono, fontSize: 12, maxWidth: 110 }} />
              {bErr && <span id={id('share-err')} style={errText}>{t(`settings.purposes.budgetError.${bErr}`)}</span>}
            </div>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <Switch on={draft.differentFromAuthor} disabled={pending} label={t('settings.purposes.differentFromAuthor')}
              onChange={(v) => set({ differentFromAuthor: v })} />
            <span>
              <span style={{ fontFamily: 'var(--font-body)', fontSize: 12, color: 'var(--text)' }}>{t('settings.purposes.differentFromAuthor')}</span>
              <span style={{ ...hint, display: 'block' }}>{t('settings.purposes.differentFromAuthorHint')}</span>
            </span>
          </div>

          {error && (
            <div role="alert" data-testid="purpose-error" style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
              <span style={errText}>{error.message}</span>
              {retryable && (
                <button type="button" className="btn btn-ghost btn-sm" onClick={() => onSave(purpose.id, draft)} disabled={pending || !!bErr}>
                  {t('settings.purposes.retry')}
                </button>
              )}
            </div>
          )}

          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <button type="button" className="btn btn-primary btn-sm" disabled={pending || !dirty || !!bErr}
              onClick={() => onSave(purpose.id, draft)} style={{ gap: 6 }}>
              {pending && <Loader2 size={11} className="spin" aria-hidden="true" />}
              {pending ? t('settings.purposes.saving') : t('settings.purposes.save')}
            </button>
            <button type="button" className="btn btn-ghost btn-sm" disabled={pending || !dirty}
              onClick={() => { if (error) onDismissError(purpose.id); setDraft(draftFrom(purpose)) }}>
              {t('settings.purposes.discard')}
            </button>
            <span style={{ flex: 1 }} />
            <button type="button" className="btn btn-ghost btn-sm" disabled={pending || purpose.isDefault} onClick={doReset}>
              {t('settings.purposes.reset')}
            </button>
          </div>
        </div>
      )}
    </li>
  )
}

// AddModel picks one more pair from the validated roster, grouped by system.
// Only ok pairs can be chosen; the others stay listed with their status so the
// operator sees why they cannot.
function AddModel({ idBase, roster, chosen, disabled, onPick }) {
  const { t } = useTranslation()
  const groups = groupRoster(roster)
  const usable = roster.some(r => r.status === 'ok' && !chosen.has(pairKey(r)))
  return (
    <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
      <label htmlFor={idBase} style={{ ...hint, color: 'var(--text-secondary)' }}>{t('settings.purposes.addModel')}</label>
      <select id={idBase} className="form-select" value="" disabled={disabled || roster.length === 0}
        onChange={e => onPick(e.target.value)} style={{ flex: '1 1 200px', minWidth: 0, maxWidth: 360 }}>
        <option value="">{usable ? t('settings.purposes.choose') : t('settings.purposes.noneLeft')}</option>
        {groups.map(g => (
          <optgroup key={g.runtime} label={g.runtime}>
            {g.models.map(m => {
              const key = pairKey(m)
              const picked = chosen.has(key)
              const ok = m.status === 'ok'
              return (
                <option key={key} value={key} disabled={!ok || picked}>
                  {m.model}{!ok ? ` (${t(`settings.purposes.status.${m.status}`)})` : picked ? ` (${t('settings.purposes.added')})` : ''}
                </option>
              )
            })}
          </optgroup>
        ))}
      </select>
    </div>
  )
}

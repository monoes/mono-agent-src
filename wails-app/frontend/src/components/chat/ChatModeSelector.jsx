import { useTranslation } from 'react-i18next'
import { Rocket } from 'lucide-react'

// The chat mode of a coder chat: off, mission or open-ended. It is chosen before the first message and locked
// after it. State comes from useChatMode; the CLI decides which modes are available, why a disabled one is disabled
// and when the mode is locked, and this only shows it: a disabled mode keeps its reason as a tooltip on the radio itself (title and aria-description) and as a
// visible line under the selector, and the whole selector has one once it is locked.

const mono = 'var(--font-mono)'

export function ChatModeChip({ state }) {
  const { t } = useTranslation()
  if (!state?.mode) return null
  const on = state.mode !== 'off'
  return (
    <span data-testid="chat-mode-chip" data-mode={state.mode} title={t('chatMode.chipTitle', { mode: t(`chatMode.${state.mode}`) })} style={{
      fontFamily: mono, fontSize: 9.5, color: on ? 'var(--green)' : 'var(--text-muted)', border: `1px solid ${on ? 'var(--green)' : 'var(--border)'}`,
      borderRadius: 999, padding: '1px 7px', whiteSpace: 'nowrap', display: 'inline-flex', alignItems: 'center', gap: 3,
    }}>
      {on && <Rocket size={9} aria-hidden="true" />} {t(`chatMode.${state.mode}`)}
    </span>
  )
}

export function ChatModeSelector({ chatMode }) {
  const { t } = useTranslation()
  const { state, busy, error, set, checking, checkOpen } = chatMode
  if (!state && !error) return null
  const locked = !!state?.locked
  const lockedWhy = state?.reason || t('chatMode.locked')
  return (
    <>
      <span role="radiogroup" aria-label={t('chatMode.label')} data-testid="chat-mode" data-locked={locked ? 'true' : undefined}
        title={locked ? lockedWhy : t('chatMode.hint')} style={{ display: 'inline-flex', gap: 2, flexShrink: 0, fontFamily: mono, fontSize: 9 }}>
        {(state?.options || []).map(o => {
          const on = state.mode === o.mode
          const pending = !!o.pending && !o.available
          const disabled = busy || locked || (!o.available && !pending)
          const why = locked ? lockedWhy : pending ? t('chatMode.checkHint') : !o.available ? (o.reason || t('chatMode.unavailable')) : ''
          return (
            <span key={o.mode} title={why || undefined}>
              <button type="button" role="radio" aria-checked={on} disabled={disabled} data-testid={`chat-mode-${o.mode}`}
                title={why || undefined} aria-description={why || undefined}
                onClick={() => !on && (pending ? checkOpen() : set(o.mode))} className="btn btn-ghost btn-sm"
                style={{ fontFamily: mono, fontSize: 9, opacity: locked && on ? 0.9 : undefined, color: on ? 'var(--green)' : 'var(--text-muted)', borderColor: on ? 'var(--green)' : undefined }}>
                {pending && checking ? t('chatMode.checking') : t(`chatMode.${o.mode}`)}
              </button>
            </span>
          )
        })}
      </span>
      {!locked && (state?.options || []).filter(o => !o.available && !o.pending).map(o => (
        <span key={o.mode} role="status" data-testid={`chat-mode-why-${o.mode}`} style={{ flexBasis: '100%', fontFamily: mono, fontSize: 9, color: 'var(--text-muted)' }}>
          {t('chatMode.whyUnavailable', { mode: t(`chatMode.${o.mode}`), reason: o.reason || t('chatMode.unavailable') })}
        </span>
      ))}
      {locked && <span data-testid="chat-mode-reason" style={{ fontFamily: mono, fontSize: 9, color: 'var(--text-muted)' }}>{lockedWhy}</span>}
      {error && <span role="alert" data-testid="chat-mode-error" style={{ flexBasis: '100%', fontFamily: mono, fontSize: 10, color: 'var(--red)' }}>{error}</span>}
    </>
  )
}

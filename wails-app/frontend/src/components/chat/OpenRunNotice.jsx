import { useState } from 'react'
import { useTranslation } from 'react-i18next'

// What the open-ended start said that must stay on screen after its form is gone: the run started but the boss has no first
// turn. It stays until the person dismisses it, and offers the two things that help: steer the boss (type in the chat) or Stop
// (the header's own Stop, with its confirmation).

const small = { fontFamily: 'var(--font-mono)', fontSize: 11 }

// firstTurnFailedIn is true when the start's document (the CLI's, in r.output) says the boss's first turn did not queue.
export function firstTurnFailedIn(r) {
  try { return JSON.parse(r?.output || '{}')?.first_turn === 'failed' } catch { return false }
}

const root = el => el?.closest?.('[data-testid="bubble-overlay"]') || document

export function OpenRunNotice({ onDismiss }) {
  const { t } = useTranslation()
  const steer = e => root(e.currentTarget).querySelector('textarea')?.focus()
  const [noStop, setNoStop] = useState(false)
  // The header's own Stop (with its confirmation) is the one Stop; when it is not on screen the person is told where else to stop.
  const stop = e => {
    const btn = root(e.currentTarget).querySelector('[data-testid="chat-stop"] button:not([disabled])')
    if (btn) btn.click(); else setNoStop(true)
  }
  return (
    <div role="alert" data-testid="open-run-notice" style={{ ...small, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', padding: '6px 12px', borderBottom: '1px solid var(--border)', color: 'var(--red)' }}>
      <span style={{ flex: 1 }}>{t('openStart.noFirstTurn')}</span>
      <button type="button" className="btn btn-ghost btn-sm" onClick={steer}>{t('openStart.steerBoss')}</button>
      <button type="button" className="btn btn-danger btn-sm" onClick={stop}>{t('openStart.stopRun')}</button>
      {noStop && <span data-testid="open-run-notice-nostop" style={{ flexBasis: '100%' }}>{t('openStart.stopElsewhere')}</span>}
      <button type="button" className="btn btn-ghost btn-sm" onClick={onDismiss}>{t('openStart.dismiss')}</button>
    </div>
  )
}

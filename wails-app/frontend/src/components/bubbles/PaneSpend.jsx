import { useTranslation } from 'react-i18next'
import { money } from '../settings/aitaskBudgetApi.js'

const mono = 'var(--font-mono)'
const note = { flexBasis: '100%', fontFamily: mono, fontSize: 10, color: 'var(--text-muted)' }

// lineKey picks the sentence for the CLI's cap (`chat history spend-cap`).
function lineKey(cap) {
  if (cap === null) return 'bubbles.pane.spendUnknown'
  if (!cap.bounded) return 'bubbles.pane.spendNone'
  if (cap.source === 'session') return cap.usedUp ? 'bubbles.pane.spendSessionUsedUp' : 'bubbles.pane.spendSession'
  if (cap.source === 'task') return cap.usedUp ? 'bubbles.pane.spendTaskUsedUp' : 'bubbles.pane.spendTask'
  if (cap.source === 'wrapup') return cap.usedUp ? 'bubbles.pane.spendWrapUpUsedUp' : 'bubbles.pane.spendWrapUp'
  return 'bubbles.pane.spendMessage'
}

// SpendValue says what bounds the chat's next message. It only renders the
// CLI's answer: undefined while loading, null when it could not be read.
export function SpendValue({ cap }) {
  const { t } = useTranslation()
  if (cap === undefined) return <>{t('bubbles.loading')}</>
  return <>{t(lineKey(cap), { cap: money(cap?.capUsd), total: money(cap?.totalUsd) })}</>
}

// SpendNotes: a runtime that can't hold the cap, and what a dynamic org's
// workers may spend besides.
export function SpendNotes({ cap }) {
  const { t } = useTranslation()
  if (!cap) return null
  return (
    <>
      {cap.unenforceableOn && (
        <span role="note" style={{ ...note, color: '#fbbf24' }}>
          {t(cap.source === 'message' ? 'bubbles.pane.spendDropped' : 'bubbles.pane.spendRefused', { runtime: cap.unenforceableOn })}
        </span>
      )}
      {cap.workersCapUsd > 0 && <span style={note}>{t('bubbles.pane.spendWorkers', { cap: money(cap.workersCapUsd) })}</span>}
    </>
  )
}

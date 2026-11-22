import { useTranslation } from 'react-i18next'
import { AlertTriangle } from 'lucide-react'

// Why a Human-in-Loop item's auto_decide did not decide it: the CLI stores
// the reason in node_config.auto_decide_skipped (internal/nodes/control
// human_in_loop_auto.go). The item is pending for a person either way; this
// line only says why Jev left it.

export function autoDecideSkipped(item) {
  const r = item?.node_config?.auto_decide_skipped
  return typeof r === 'string' ? r.trim() : ''
}

// skipText rewrites the two reasons a decision provider gives into a
// sentence; any other reason is shown as stored.
//   jev: decision provider "x" did not answer (<class>)
//   a decision provider on another host is not used from a run an AI started
//   or wrote (<why>): provider "x" is at <url>; name a provider on this
//   machine or typesafe, or run the workflow yourself
export function skipText(reason, t) {
  const noAnswer = /^jev: decision provider "([^"]+)" did not answer \(([^)]*)\)$/.exec(reason)
  if (noAnswer) return t('hil.autoDecide.noAnswer', { name: noAnswer[1], cause: noAnswer[2] })
  const remote = /a decision provider on another host is not used from a run an AI started or wrote \(([^)]*)\): provider "([^"]+)" is at (\S+?);/.exec(reason)
  if (remote) return t('hil.autoDecide.remoteFromAIRun', { why: remote[1], name: remote[2], url: remote[3] })
  return reason
}

export function AutoDecideSkipped({ item }) {
  const { t } = useTranslation()
  const reason = autoDecideSkipped(item)
  if (!reason) return null
  return (
    <div data-testid="hil-auto-decide-skipped" style={{
      display: 'flex', alignItems: 'flex-start', gap: 6, padding: '6px 16px',
      fontSize: 11.5, lineHeight: 1.5, color: '#f59e0b', background: 'rgba(245,158,11,0.06)',
      borderBottom: '1px solid rgba(245,158,11,0.15)', wordBreak: 'break-word',
    }}>
      <AlertTriangle size={13} style={{ flexShrink: 0, marginTop: 2 }} />
      <span><strong style={{ fontWeight: 600 }}>{t('hil.autoDecide.label')}</strong> {skipText(reason, t)}</span>
    </div>
  )
}

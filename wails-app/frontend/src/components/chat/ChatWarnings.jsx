import { useTranslation } from 'react-i18next'
import { AlertTriangle } from 'lucide-react'

function ChatWarning({ role, testId, text }) {
  return (
    <div role={role} data-testid={testId} style={{
      display: 'flex', alignItems: 'flex-start', gap: 6, flexShrink: 0, margin: '6px 12px', padding: '6px 8px',
      borderRadius: 6, background: 'rgba(251,191,36,0.1)', border: '1px solid rgba(251,191,36,0.45)',
      color: '#fbbf24', fontFamily: 'var(--font-mono)', fontSize: 10.5, lineHeight: 1.5,
    }}>
      <AlertTriangle size={12} aria-hidden="true" style={{ marginTop: 2, flexShrink: 0 }} />
      <span>{text}</span>
    </div>
  )
}

// FullAccessWarning is shown wherever a new chat is about to start at full
// access. A caller that already has an alert of its own passes role="status".
export function FullAccessWarning({ role = 'alert' }) {
  const { t } = useTranslation()
  return <ChatWarning role={role} testId="full-access-warning" text={t('chatWarnings.fullAccess')} />
}

// UnsandboxedWarning marks a turn that runs without a sandbox. The CLI decides
// (its agent.sandbox notice is a warning); this only renders. A status, not an
// alert: a transcript read back from history can hold many.
export function UnsandboxedWarning() {
  const { t } = useTranslation()
  return <ChatWarning role="status" testId="unsandboxed-warning" text={t('chatWarnings.unsandboxed')} />
}

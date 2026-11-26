import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle } from 'lucide-react'
import { api } from '../../services/api.js'

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

// accessWarningKey is the warning (a chatWarnings key) a new chat at `access`
// is due, '' for none. status is `coder status`: while its box is off (an older
// CLI reports none, and has none) a coding or workspace turn runs with the same
// full runtime access as a full one. A status that could not be read says
// nothing about the box.
export function accessWarningKey(access, status) {
  if (access === 'full') return 'fullAccess'
  const boxOff = !!status && !status.error && (status.box || 'off') === 'off'
  return boxOff && (access === 'coding' || access === 'workspace') ? 'boxOff' : ''
}

// AccessWarning is shown wherever a new chat is about to start able to run
// anything. A caller that already has an alert of its own passes role="status".
export function AccessWarning({ access, status, role = 'alert' }) {
  const { t } = useTranslation()
  const key = accessWarningKey(access, status)
  if (!key) return null
  return <ChatWarning key={access} role={role} testId={key === 'fullAccess' ? 'full-access-warning' : 'box-off-warning'}
    text={t(`chatWarnings.${key}`, { level: t(`bubbles.pane.level.${access}`) })} />
}

// FetchedAccessWarning is AccessWarning for a view that has no `coder status`
// of its own: it reads it once, and only for the levels the box decides.
export function FetchedAccessWarning({ access }) {
  const [status, setStatus] = useState(null)
  const boxDecides = access === 'coding' || access === 'workspace'
  useEffect(() => {
    if (!boxDecides) return undefined
    let live = true
    Promise.resolve().then(() => api.coderStatus()).then(s => { if (live) setStatus(s || null) }).catch(() => {})
    return () => { live = false }
  }, [boxDecides])
  return <AccessWarning access={access} status={status} />
}

// UnsandboxedWarning marks a turn that runs without a sandbox. The CLI decides
// (its agent.sandbox notice is a warning); this only renders. A status, not an
// alert: a transcript read back from history can hold many.
export function UnsandboxedWarning() {
  const { t } = useTranslation()
  return <ChatWarning role="status" testId="unsandboxed-warning" text={t('chatWarnings.unsandboxed')} />
}

// UnconfinedWorkerWarning marks a dynamic org's research worker that nothing
// holds to reading (confinement "write-lease").
export function UnconfinedWorkerWarning() {
  const { t } = useTranslation()
  return <ChatWarning role="status" testId="unconfined-worker-warning" text={t('chatWarnings.workerUnsandboxed')} />
}

import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, X } from 'lucide-react'
import { onApiError } from '../services/api.js'
import { isAgentNotSetup, withoutAgentSetupMarker } from '../lib/agentSetup.js'
import AgentSetupLink from './AgentSetupLink.jsx'

// Listens to the api error bus and shows transient toasts, so a failed backend
// call is visible instead of silently degrading a page to empty data. A
// failure because the AI agent is not set up gets a link to the AI agents
// page (onNavigate), and stays up longer so there is time to use it; so does
// one carrying an action button ({ label, run }, e.g. "Activate & run").
export default function Toasts({ onNavigate }) {
  const { t } = useTranslation()
  const [toasts, setToasts] = useState([])
  const seq = useRef(0)

  const dismiss = useCallback((id) => {
    setToasts(prev => prev.filter(t => t.id !== id))
  }, [])

  useEffect(() => {
    return onApiError(({ op, message, code, action, title }) => {
      const id = ++seq.current
      const agentSetup = isAgentNotSetup({ code, message })
      setToasts(prev => {
        // Collapse a rapid burst of the same op into one toast.
        const filtered = prev.filter(t => t.op !== op)
        return [...filtered, { id, op, title, message: withoutAgentSetupMarker(message), agentSetup, action }].slice(-4)
      })
      // A toast with something to click stays up long enough to click it.
      setTimeout(() => dismiss(id), agentSetup || action ? 15000 : 6000)
    })
  }, [dismiss])

  if (!toasts.length) return null

  return (
    <div style={{
      position: 'fixed', bottom: 16, right: 16, zIndex: 10000,
      display: 'flex', flexDirection: 'column', gap: 8, maxWidth: 380,
    }}>
      {toasts.map(toast => (
        <div
          key={toast.id}
          role="alert"
          style={{
            display: 'flex', alignItems: 'flex-start', gap: 10,
            padding: '10px 12px',
            background: '#1a0f12', border: '1px solid rgba(239,68,68,0.35)',
            borderRadius: 8, boxShadow: '0 8px 24px rgba(0,0,0,0.5)',
            fontFamily: 'var(--font-mono)', fontSize: 11.5, color: '#fca5a5',
          }}
        >
          <AlertTriangle size={14} style={{ flexShrink: 0, marginTop: 1, color: '#ef4444' }} />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ color: '#fecaca', fontWeight: 600, marginBottom: 2 }}>{toast.title || t('toasts.failed', { op: toast.op })}</div>
            <div style={{ color: '#f8a5a5', wordBreak: 'break-word', whiteSpace: 'pre-line', opacity: 0.85 }}>{toast.message}</div>
            {toast.agentSetup && <AgentSetupLink onNavigate={onNavigate} onDone={() => dismiss(toast.id)} />}
            {toast.action && (
              <button
                className="btn btn-secondary btn-sm"
                style={{ marginTop: 8 }}
                onClick={() => { dismiss(toast.id); toast.action.run() }}
              >{toast.action.label}</button>
            )}
          </div>
          <button
            onClick={() => dismiss(toast.id)}
            aria-label={t('toasts.dismiss')}
            style={{ background: 'none', border: 'none', color: '#fca5a5', cursor: 'pointer', flexShrink: 0, padding: 0, lineHeight: 1 }}
          >
            <X size={13} />
          </button>
        </div>
      ))}
    </div>
  )
}

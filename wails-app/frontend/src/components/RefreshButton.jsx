import { RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import i18nOr from '../lib/i18nOr.js'

// Standardizes the refresh-button markup already established in
// Dashboard.jsx/Agents.jsx (btn btn-ghost btn-sm + RefreshCw icon, spin
// animation while `loading`) — previously copy-pasted per page, never
// extracted into a shared component.
export default function RefreshButton({ onClick, loading, label }) {
  useTranslation() // render again on a language change
  return (
    <button className="btn btn-ghost btn-sm" onClick={onClick} disabled={loading} style={{ gap: 5 }}>
      <RefreshCw size={13} style={{ animation: loading ? 'spin 0.7s linear infinite' : 'none' }} />
      {label ?? i18nOr('ui6PagesImages.refresh')}
    </button>
  )
}

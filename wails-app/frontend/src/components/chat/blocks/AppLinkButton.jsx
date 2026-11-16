import { useTranslation } from 'react-i18next'
import { ExternalLink } from 'lucide-react'
import { useBlockEnv } from './context.js'
import { COLOR } from './ui.js'

// A monoagent:// link as a button (never an anchor). Calls the host's
// optional onAppLink(href); without one a click does nothing.
export function AppLinkButton({ href, label }) {
  const { t } = useTranslation()
  const { onAppLink } = useBlockEnv()
  const text = label || t('chatBlocks.open')
  return (
    <button type="button" title={href} aria-label={`${text}: ${href}`} onClick={() => onAppLink?.(href)}
      style={{ display: 'inline-flex', alignItems: 'center', gap: 3, background: 'transparent', border: `1px solid ${COLOR.border}`, borderRadius: 4, padding: '1px 6px', color: COLOR.accent, fontFamily: 'var(--font-mono)', fontSize: 10, cursor: onAppLink ? 'pointer' : 'default' }}>
      <ExternalLink size={10} aria-hidden="true" />{text}
    </button>
  )
}

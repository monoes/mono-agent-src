import { useTranslation } from 'react-i18next'
import { ShieldAlert } from 'lucide-react'
import { clip } from './controlRoomModel.js'

// Shown whenever the audit chain is "no" or "unknown". The tree, the spend and the feed are not drawn then: the
// numbers cannot be trusted, and a page that showed them would look healthy. The reason is put in plain words.
export default function BadChainBanner({ chain, reason }) {
  const { t } = useTranslation()
  return (
    <div className="cr-banner" role="alert" data-testid="cr-bad-chain" data-chain={chain} data-reason={clip(reason, 40)}>
      <ShieldAlert size={18} aria-hidden="true" />
      <div>
        <strong>{t(chain === 'no' ? 'controlRoom.badChain.titleNo' : 'controlRoom.badChain.titleUnknown')}</strong>
        <p>{t('controlRoom.badChain.body')}</p>
        {reason ? <p data-testid="cr-reason">{t(`controlRoom.reason.${clip(reason, 40)}`, { defaultValue: t('controlRoom.reason.other') })}</p> : null}
        {reason ? <p className="cr-mono">{clip(reason, 80)}</p> : null}
      </div>
    </div>
  )
}

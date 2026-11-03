import { useTranslation } from 'react-i18next'
import { ShieldQuestion } from 'lucide-react'
import { clip } from './controlRoomModel.js'

// The chain is consistent but the stop witness could not confirm its head, so a cut tail cannot be ruled out. The
// data below is shown, under this amber banner; the run is never drawn as healthy.
export default function UnanchoredBanner({ reason }) {
  const { t } = useTranslation()
  return (
    <div className="cr-banner cr-banner-amber" role="alert" data-testid="cr-unanchored" data-reason={clip(reason, 40)}>
      <ShieldQuestion size={18} aria-hidden="true" />
      <div>
        <strong>{t('controlRoom.unanchored.title')}</strong>
        <p>{t('controlRoom.unanchored.body')}</p>
        <p data-testid="cr-reason">{t(`controlRoom.reason.${clip(reason, 40)}`, { defaultValue: t('controlRoom.reason.other') })}</p>
        {reason ? <p className="cr-mono">{clip(reason, 80)}</p> : null}
      </div>
    </div>
  )
}

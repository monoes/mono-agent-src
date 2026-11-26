import { useTranslation } from 'react-i18next'
import SendToCoderButton from './SendToCoderButton.jsx'
import { aitaskApi } from './SendToCoderApi.js'
import t from '../lib/i18nOr.js'

// ApplyWithCoderButton has the coder fill in an application's form in the
// person's browser (`application fill start`). It never submits: the task
// header says "filled, waiting for you to submit" when it is done. With coder
// mode off it says why and the regular Apply beside it still works.
export default function ApplyWithCoderButton({ applicationId, ...rest }) {
  useTranslation()
  return (
    <SendToCoderButton
      label={t('ui6PagesApps.apply_with_coder')}
      kind="application-apply"
      applicationId={applicationId}
      start={() => aitaskApi.startFill(applicationId)}
      doneLabel={t('ui6PagesApps.done_label')}
      origin={{ page: 'applications', label: t('ui6PagesApps.applications') }}
      {...rest}
    />
  )
}

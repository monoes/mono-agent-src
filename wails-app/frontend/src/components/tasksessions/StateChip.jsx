import { useTranslation } from 'react-i18next'
import { chipState } from '../../lib/taskSessionModel.js'

// The state of a session as a chip: the word is always there, so the colour is never the only signal.
export default function StateChip({ session }) {
  const { t } = useTranslation()
  const s = chipState(session)
  return <span className={`ts-chip ts-chip--${s}`} data-testid="ts-state">{t(`tasks.session.state.${s}`)}</span>
}

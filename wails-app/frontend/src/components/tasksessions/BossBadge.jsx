import { useTranslation } from 'react-i18next'

// The mark of the session the operator recorded as the boss of the profile's open-ended run. Nothing for any other session.
export default function BossBadge({ session }) {
  const { t } = useTranslation()
  if (!session?.boss) return null
  return <span className="ts-chip ts-chip--boss" data-testid="ts-boss">{t('tasks.session.boss')}</span>
}

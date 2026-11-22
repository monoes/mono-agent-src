import { useTranslation } from 'react-i18next'

// Problem shows one callStep failure inside a step: the shell's sign-in line
// for a login-required answer, otherwise the binding's own message. Nothing
// for a success or null.
export function Problem({ r }) {
  const { t } = useTranslation()
  if (!r || r.ok) return null
  if (r.loginRequired) return <p className="onb-error" role="alert">{t('onboarding.shell.signIn')}</p>
  return <p className="onb-error" role="alert">{r.message}</p>
}

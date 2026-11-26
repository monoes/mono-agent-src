// t() for plain modules, and for components whose tests mock react-i18next without i18n:
// the app's i18next instance (set up by i18n.js) translates; where nothing set it up, the
// English entry of en.json is used. It does not import i18n.js, which needs the real
// react-i18next. A component still calls useTranslation, to render again on a language change.
import i18n from 'i18next'
import en from '../locales/en.json'

export default function i18nOr(key, vars) {
  if (i18n.isInitialized) return i18n.t(key, vars)
  const plural = vars?.count === undefined ? '' : vars.count === 1 ? '_one' : '_other'
  const text = (key + plural).split('.').reduce((node, part) => node?.[part], en) ?? key
  return text.replace(/\{\{(\w+)\}\}/g, (_, name) => vars?.[name])
}

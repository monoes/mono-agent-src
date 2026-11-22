// Minimal i18next + react-i18next setup for the pilot locale rollout.
// See docs/i18n.md for the "Adding a locale" guide — dropping a new
// src/locales/<lang>.json file and adding it to `resources` below is the
// entire mechanical extension path for a new GUI language.
import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import en from './locales/en.json'
import es from './locales/es.json'

const STORAGE_KEY = 'monoagent-lang'
const SUPPORTED = ['en', 'es']

function detectInitialLanguage() {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (SUPPORTED.includes(stored)) return stored
  } catch { /* localStorage unavailable (e.g. sandboxed webview) */ }
  const browser = (typeof navigator !== 'undefined' && navigator.language) || 'en'
  return browser.startsWith('es') ? 'es' : 'en'
}

i18n.use(initReactI18next).init({
  resources: {
    en: { translation: en },
    es: { translation: es },
  },
  lng: detectInitialLanguage(),
  fallbackLng: 'en',
  interpolation: { escapeValue: false }, // React already escapes.
})

i18n.on('languageChanged', (lng) => {
  try {
    localStorage.setItem(STORAGE_KEY, lng)
  } catch { /* localStorage unavailable */ }
})

export default i18n

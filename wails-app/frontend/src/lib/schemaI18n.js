// Translations for the schema-driven workflow-step settings. Node schemas come from the CLI in
// English; locales/schema/<lang>.json overrides their texts by node type + field key (sub-node
// inputs are keyed "@<handle>") and is loaded only while that language is in use. A text with no
// entry stays as the schema wrote it.
import { useEffect, useReducer } from 'react'
import { useTranslation } from 'react-i18next'

const LOADERS = { es: () => import('../locales/schema/es.json') }
const loaded = {}

export async function loadSchemaCatalog(lang) {
  if (!LOADERS[lang]) return null
  if (!loaded[lang]) loaded[lang] = (await LOADERS[lang]()).default
  return loaded[lang]
}

// useSchemaCatalog is the catalog of the current language: null for English, and while loading.
export function useSchemaCatalog() {
  const { i18n } = useTranslation()
  const lang = String(i18n?.language || 'en').split('-')[0]
  const [, loadedNow] = useReducer(n => n + 1, 0)
  useEffect(() => {
    if (!LOADERS[lang] || loaded[lang]) return
    let alive = true
    // A catalog that fails to load leaves the English schema texts in place.
    loadSchemaCatalog(lang).then(() => { if (alive) loadedNow() }).catch(() => {})
    return () => { alive = false }
  }, [lang])
  return loaded[lang] || null
}

// localizeFields is fields with their label, help and other shown texts in the catalog's language.
// Only a text the schema has is replaced. Without a catalog entry it returns fields itself.
export function localizeFields(catalog, nodeType, fields) {
  const node = catalog?.[nodeType]
  if (!node || !Array.isArray(fields)) return fields
  return fields.map(f => {
    const texts = node[f?.key]
    if (!texts) return f
    const out = { ...f }
    for (const attr of ['label', 'help', 'placeholder']) {
      if (out[attr] && texts[attr]) out[attr] = texts[attr]
    }
    if (out.resource?.create_label && texts.createLabel) {
      out.resource = { ...out.resource, create_label: texts.createLabel }
    }
    if (out.helpContent) {
      out.helpContent = out.helpContent.map((s, i) => (texts[`section${i}`] ? { ...s, title: texts[`section${i}`] } : s))
    }
    return out
  })
}

// localizeSubInputs is a canvas node with its sub-node port labels in the catalog's language.
export function localizeSubInputs(catalog, node) {
  const texts = catalog?.[node?.subtype]
  if (!texts || !node.subInputs?.length) return node
  return {
    ...node,
    subInputs: node.subInputs.map(s => {
      const label = texts[`@${s.handle}`]?.label
      return label ? { ...s, label } : s
    }),
  }
}

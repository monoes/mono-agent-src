// Rules the workflow editor's inspector applies to a node: which credential
// or browser session it takes, and which text fields may take an image or
// file from the vault. Pure functions so they are unit-testable without the
// canvas.

// LEGACY FALLBACK for API nodes whose schemas don't set credential_platform
// yet. Browser automation nodes never need an entry here: their platform is
// the automation id, read from the installed automation list.
export const CREDENTIAL_PLATFORMS = {
  'service.github': 'github',
  'service.notion': 'notion',
  'service.airtable': 'airtable',
  'service.jira': 'jira',
  'service.linear': 'linear',
  'service.asana': 'asana',
  'service.stripe': 'stripe',
  'service.shopify': 'shopify',
  'service.salesforce': 'salesforce',
  'service.hubspot': 'hubspot',
  'service.google_sheets': 'google_sheets',
  'service.gmail': 'gmail',
  'service.google_drive': 'google_drive',
  'comm.slack': 'slack',
  'comm.discord': 'discord',
  'comm.twilio': 'twilio',
  'comm.whatsapp': 'whatsapp',
  'db.postgres': 'postgresql',
  'db.mysql': 'mysql',
  'db.mongodb': 'mongodb',
  'db.redis': 'redis',
  'service.huggingface': 'huggingface',
}

// automationIdsFrom turns `automation list --all --json` into the set of
// installed automation ids (removed built-ins included: their nodes may still
// sit in a workflow and still take a session).
export function automationIdsFrom(res) {
  const list = Array.isArray(res?.automations) ? res.automations : []
  return new Set(list.map(a => a.id).filter(Boolean))
}

// browserAutomationOf returns the automation id when the node type is an
// action of an installed browser automation ("<automation>.<action>").
export function browserAutomationOf(nodeType, automationIds) {
  const prefix = String(nodeType || '').split('.')[0]
  return prefix && automationIds?.has(prefix) ? prefix : null
}

// derivePlatformId picks the platform whose credentials or sessions the
// node's picker lists: the schema's credential_platform first, then the
// automation the node belongs to, then the legacy API map.
export function derivePlatformId(node, schema, automationIds) {
  if (!node) return null
  if (schema?.credential_platform) return schema.credential_platform
  const automation = browserAutomationOf(node.subtype, automationIds)
  if (automation) return automation
  return CREDENTIAL_PLATFORMS[node.subtype] || null
}

// isSessionPicker: the picker lists browser sessions (not API credentials)
// when the node is an action of a browser automation.
export function isSessionPicker(node, automationIds) {
  return !!browserAutomationOf(node?.subtype, automationIds)
}

const MEDIA_HINTS = new Set(['image', 'file', 'path', 'media', 'photo', 'video'])
const MEDIA_WORDS = new Set(['image', 'images', 'img', 'photo', 'photos', 'picture', 'pictures', 'media', 'avatar', 'thumbnail', 'attachment', 'attachments', 'video', 'videos', 'cover', 'banner'])

// isMediaField reports whether a text field may take an image or file from
// the vault (the 🖼 picker and @-autocomplete): an explicit type/format/
// widget hint, or a key that names a media value (image_url, media, photo,
// file_path, …) — but not an id or name of one (video_id, file_name).
export function isMediaField(field) {
  if (!field) return false
  const hints = [field.type, field.format, field.widget, field.ui?.widget, field.ui?.kind].map(h => String(h || '').toLowerCase())
  if (hints.some(h => MEDIA_HINTS.has(h))) return true
  const tokens = String(field.key || '')
    .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter(Boolean)
  if (!tokens.length) return false
  const last = tokens[tokens.length - 1]
  if (['id', 'ids', 'name', 'names', 'count', 'type'].includes(last)) return false
  if (tokens.some(t => MEDIA_WORDS.has(t))) return true
  // file_path / local_path / file: a local file to upload.
  return (tokens.includes('file') && (last === 'path' || last === 'file')) || tokens.join('_') === 'local_path'
}

// credentialPickerMode says how the inspector's connection picker lists
// connections: 'platform' for the derived platform's connections, 'any'
// when the schema has a credential_picker field but names no platform
// (http.request and http.graphql take a connection of any platform), null
// when the node takes no connection.
export function credentialPickerMode(platformId, schema) {
  if (platformId) return 'platform'
  return (schema?.fields || []).some(f => f?.type === 'credential_picker') ? 'any' : null
}

// connectionLabel is the name the picker shows for a saved connection.
export function connectionLabel(c) {
  return c?.label || c?.Label || c?.AccountID || c?.account_id || c?.platform || c?.id || c?.ID || ''
}

// groupConnectionsByPlatform groups connections under their platform for
// the any-platform picker: platforms in name order, labels sorted in each.
export function groupConnectionsByPlatform(conns) {
  const groups = new Map()
  for (const c of Array.isArray(conns) ? conns : []) {
    const p = c?.platform || c?.Platform || 'other'
    if (!groups.has(p)) groups.set(p, [])
    groups.get(p).push(c)
  }
  return [...groups.keys()].sort().map(platform => ({
    platform,
    connections: groups.get(platform).slice().sort((a, b) => connectionLabel(a).localeCompare(connectionLabel(b))),
  }))
}

// fieldIsVisible applies a field's visibility rule to the node config.
// visible_for (API package operation fields) is a list of conditions such
// as {resource, operation}: the field shows when every key of one of them
// matches. Otherwise depends_on {key, values} shows it when that one key
// has one of the values. A key the config doesn't set yet counts as its
// field's default.
export function fieldIsVisible(field, config, fields) {
  const valueOf = key => {
    let v = config?.[key]
    if (v === undefined) v = (fields || []).find(f => f?.key === key)?.default
    return String(v ?? '')
  }
  if (Array.isArray(field?.visible_for) && field.visible_for.length > 0) {
    return field.visible_for.some(cond => cond && typeof cond === 'object' &&
      Object.entries(cond).every(([k, v]) => valueOf(k) === String(v ?? '')))
  }
  const dep = field?.depends_on
  if (!dep) return true
  return (dep.values || []).map(String).includes(valueOf(dep.key ?? dep.field))
}

// isFormFieldsField: the inspector edits a schema field keyed formFields
// with the form builder (FormFieldsEditor), whatever node it is on.
export function isFormFieldsField(field) {
  return field?.key === 'formFields'
}

// A collection field (n8n "additional fields") holds an object of optional
// settings; the inspector edits it as JSON text.
export function collectionText(value) {
  if (value == null || value === '') return ''
  return typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value)
}

// jsonTextError is why text is not valid JSON, '' when it is, is empty, or
// carries a {{ template }} (resolved at run time, so not checked here).
export function jsonTextError(text) {
  const s = String(text ?? '').trim()
  if (!s || s.includes('{{')) return ''
  try { JSON.parse(s); return '' } catch (e) { return 'Not valid JSON: ' + e.message }
}

// commitCollection turns edited collection text into the value to save.
// Invalid text is not saved (error set). A field that held an object keeps
// an object; one that held text (or nothing) keeps the text as typed. A
// {{ template }} is saved as text.
export function commitCollection(text, asObject) {
  const s = String(text ?? '').trim()
  if (!s) return { value: asObject ? {} : '', error: '' }
  if (s.includes('{{')) return { value: String(text), error: '' }
  let parsed
  try { parsed = JSON.parse(s) } catch (e) { return { value: undefined, error: 'Not valid JSON: ' + e.message } }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { value: undefined, error: 'Must be a JSON object, e.g. { "key": "value" }' }
  }
  return { value: asObject ? parsed : String(text), error: '' }
}

// curlImportConfig is the config an imported curl command leaves on a
// node: every schema field back to its default (the command describes the
// whole request), the saved connection kept, then the imported keys.
export function curlImportConfig(fields, current, imported) {
  const next = {}
  for (const f of fields || []) {
    if (f?.key && !(f.key in next)) next[f.key] = f.default ?? ''
  }
  if (current?.credential_id) next.credential_id = current.credential_id
  return { ...next, ...(imported || {}) }
}

// Rules the workflow editor's inspector applies to a node: which credential
// or browser session it takes, and which text fields may take an image or
// file from the vault. Pure functions so they are unit-testable without the
// canvas.

// LEGACY FALLBACK for API nodes whose schemas don't set credential_platform
// yet. Browser automation nodes never need an entry here: their platform is
// the automation id, read from the installed automation list.
import i18n from '../i18n.js'

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
// has one of the values and the field holding that key shows too (a field
// under a hidden one stays hidden). A key's value is the one
// fieldValueFor reads.
export function fieldIsVisible(field, config, fields) {
  return visibleIn(field, config, fields || [], new Set())
}

function visibleIn(field, config, fields, seen) {
  // seen guards against a schema whose rules loop: a key being resolved
  // reads as its stored value only.
  const valueOf = key => String(valueIn(key, config, fields, seen) ?? '')
  if (Array.isArray(field?.visible_for) && field.visible_for.length > 0) {
    return field.visible_for.some(cond => cond && typeof cond === 'object' &&
      Object.entries(cond).every(([k, v]) => valueOf(k) === String(v ?? '')))
  }
  const dep = field?.depends_on
  if (!dep) return true
  const key = dep.key ?? dep.field
  if (!(dep.values || []).map(String).includes(valueOf(key))) return false
  const holders = fields.filter(f => f?.key === key)
  if (!holders.length || seen.has(key)) return true
  const inner = new Set(seen).add(key)
  return holders.some(f => visibleIn(f, config, fields, inner))
}

// applicableIn is the field a key is read and edited through: the shown one
// when several fields share the key (an API package's operation select per
// resource, its options per operation), else the first. Where more than one
// shows, the first that takes the stored value wins.
function applicableIn(key, config, fields, seen) {
  const holders = fields.filter(f => f?.key === key)
  if (holders.length < 2 || seen.has(key)) return holders[0]
  const inner = new Set(seen).add(key)
  const shown = holders.filter(f => visibleIn(f, config, fields, inner))
  const v = config?.[key]
  return (v !== undefined && shown.find(f => !selectRejects(f, v, fields))) || shown[0] || holders[0]
}

function valueIn(key, config, fields, seen) {
  const f = applicableIn(key, config, fields, seen)
  const v = config?.[key]
  if (v === undefined) return f?.default
  return f && selectRejects(f, v, fields) ? f.default : v
}

// A select sharing its key with other fields (an operation select per
// resource) that holds a value none of its options offer — another
// resource's operation — reads as its default. A {{ template }} stays, and a
// select with a key of its own keeps whatever was stored.
function selectRejects(field, value, fields) {
  if (field?.type !== 'select' || !Array.isArray(field.options) || !field.options.length) return false
  if ((fields || []).filter(f => f?.key === field.key).length < 2) return false
  const s = String(value ?? '')
  return !s.includes('{{') && !field.options.map(String).includes(s)
}

// fieldValueFor is the value a field shows: the stored one, else the
// field's default; a shared-key select's stored value that is not one of
// its options shows as its default.
export function fieldValueFor(field, config, fields) {
  const v = config?.[field?.key]
  if (v === undefined || v === null) return field?.default
  return selectRejects(field, v, fields) ? field.default : v
}

// configDefaults is the config a freshly added node starts with. Where
// several fields share a key, the default is that of the field that shows
// under the other defaults (the operation select of the default resource),
// not whichever comes last.
export function configDefaults(fields) {
  const list = (fields || []).filter(f => f?.key)
  const cfg = {}
  for (const f of list) if (!(f.key in cfg)) cfg[f.key] = f.default ?? ''
  return settleSharedKeys(list, cfg, {}, null)
}

// configAfterChange is the node config after one field is set. Every other
// key held by several fields (operation, options, model…) whose shown field
// changed because of it is reset to the newly shown field's default:
// switching an OpenAI node from text to image picks the image operation
// select's default instead of keeping "classify". Keys held by one field
// are left as they are.
export function configAfterChange(fields, config, key, value) {
  const list = (fields || []).filter(f => f?.key)
  const before = {}
  for (const f of list) if (!(f.key in before)) before[f.key] = applicableIn(f.key, config, list, new Set())
  return settleSharedKeys(list, { ...(config || {}), [key]: value }, before, key)
}

function settleSharedKeys(list, cfg, before, fixedKey) {
  const keys = [...new Set(list.map(f => f.key))].filter(k => k !== fixedKey)
  const shared = new Set(keys.filter(k => list.filter(f => f.key === k).length > 1))
  // A reset can change which fields show, so repeat until nothing moves
  // (bounded, in case a schema's rules loop).
  for (let pass = 0; pass <= keys.length; pass++) {
    let changed = false
    for (const k of keys) {
      const f = applicableIn(k, cfg, list, new Set())
      if (!f || !visibleIn(f, cfg, list, new Set())) continue
      const moved = shared.has(k) && (!(k in before) || before[k] !== f)
      if (moved || (k in cfg && selectRejects(f, cfg[k], list))) {
        const next = f.default ?? ''
        if (cfg[k] !== next) { cfg = { ...cfg, [k]: next }; changed = true }
      }
    }
    if (!changed) break
  }
  return cfg
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
  try { JSON.parse(s); return '' } catch (e) { return i18n.t('ui6Node.notJson', { error: e.message }) }
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
  try { parsed = JSON.parse(s) } catch (e) { return { value: undefined, error: i18n.t('ui6Node.notJson', { error: e.message }) } }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { value: undefined, error: i18n.t('ui6Node.mustBeObject') }
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

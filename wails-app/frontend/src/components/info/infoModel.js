import i18nOr from '../../lib/i18nOr.js'

// Pure helpers for the Info page. The field catalog (`profile info fields`)
// is the only list of fields; nothing here names a field.

// The profile is a person or a business; its type is the TYPE_KEY field, kept
// in the "type" group, which the page shows as a toggle instead of a section.
export const TYPE_KEY = 'profile_type'
const TYPE_GROUP = 'type'

// profileType -> 'business' or 'person' (the default) from the stored entries.
export function profileType(entries) {
  const v = (entries || []).find(e => e.key === TYPE_KEY)?.value
  return String(v || '').toLowerCase() === 'business' ? 'business' : 'person'
}

// groupCatalog -> [{ id, label, fields }] in the catalog's group order, with
// each group's fields in catalog order. Only the groups of the profile's type
// (and those for both) are kept. Groups with no fields are dropped, and
// fields naming an unknown group are kept in a trailing group of their own.
export function groupCatalog(catalog, type = 'person') {
  const shown = g => g.id !== TYPE_GROUP && (!g.scope || g.scope === type)
  const groups = (catalog?.groups || []).filter(shown).map(g => ({ id: g.id, label: g.label, fields: [] }))
  const byId = new Map(groups.map(g => [g.id, g]))
  const hidden = new Set((catalog?.groups || []).filter(g => !shown(g)).map(g => g.id))
  for (const f of catalog?.fields || []) {
    if (hidden.has(f.group)) continue
    let g = byId.get(f.group)
    if (!g) {
      g = { id: f.group, label: f.group, fields: [] }
      byId.set(f.group, g)
      groups.push(g)
    }
    g.fields.push(f)
  }
  return groups.filter(g => g.fields.length > 0)
}

export function entryMap(entries) {
  return new Map((entries || []).map(e => [e.key, e]))
}

export const hasValue = (entry) => !!entry && entry.value !== ''

// leftEmpty counts the fields an AI fill could still fill: photos and the
// card are never AI-filled, so they do not count.
export function leftEmpty(catalog, entries, type = 'person') {
  const map = entryMap(entries)
  const inType = new Set(groupCatalog(catalog, type).flatMap(g => g.fields.map(f => f.key)))
  return (catalog?.fields || [])
    .filter(f => inType.has(f.key) && f.kind !== 'photo' && !f.card && !hasValue(map.get(f.key))).length
}

export function errorText(e) {
  const m = (e && e.message) || String(e ?? '')
  return m.replace(/^Error:\s*/i, '').trim() || i18nOr('ui6PagesMisc.unknown_error')
}

export const isPdfDataUrl = (u) => typeof u === 'string' && u.startsWith('data:application/pdf')

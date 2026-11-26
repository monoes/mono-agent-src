import t from './i18nOr.js'
// Sorting, labelling and filtering for the Documents page. Pure functions,
// kept out of Documents.jsx so they can be unit-tested without rendering.


// Human labels (locale keys) for vault_documents.source. Anything not listed
// (a custom `upload-document --source`) is shown as stored.
const SOURCE_LABEL_KEYS = {
  extension: 'ui6PagesDocs.source_extension',
  crawl: 'ui6PagesDocs.source_crawl',
  monobrowse: 'ui6PagesDocs.source_monobrowse',
  upload: 'ui6PagesDocs.source_upload',
  discovered: 'ui6PagesDocs.source_discovered',
  generated: 'ui6PagesDocs.source_generated',
}

export function sourceLabel(source) {
  if (!source) return t('ui6PagesDocs.source_unknown')
  return Object.hasOwn(SOURCE_LABEL_KEYS, source) ? t(SOURCE_LABEL_KEYS[source]) : source
}

// True for a row backed by a browser capture (an inbox envelope) rather
// than a single file.
export function isCapture(doc) {
  return !!doc?.capture_dir
}

function extOf(name) {
  const base = (name || '').split(/[\\/]/).pop() || ''
  const dot = base.lastIndexOf('.')
  return dot > 0 && dot < base.length - 1 ? base.slice(dot + 1).toLowerCase() : ''
}

// documentType is the file type shown in the Type column: the extension of
// the file the row actually opens. For a capture that is its primary
// artifact (readable.md, page.pdf, page.mhtml), since its name is the page
// title and carries no extension.
export function documentType(doc) {
  return extOf(doc?.path) || extOf(doc?.filename) || ''
}

// viewerFilename is the name FileViewerModal and its kind check get: a
// capture's title plus its artifact's extension, so a readable.md capture
// previews as markdown while still being titled after the page.
export function viewerFilename(doc) {
  if (!isCapture(doc)) return doc.filename
  const ext = documentType(doc)
  return ext ? `${doc.filename}.${ext}` : doc.filename
}

export const SORT_KEYS = ['name', 'type', 'source', 'date']
export const DEFAULT_SORT = { key: 'date', dir: 'desc' }
const SORT_STORAGE_KEY = 'monoagent.documents.sort'

const collator = new Intl.Collator(undefined, { sensitivity: 'base', numeric: true })

function sortValue(doc, key) {
  switch (key) {
    case 'name': return doc.filename || ''
    case 'type': return documentType(doc)
    case 'source': return sourceLabel(doc.source)
    // created_at is "YYYY-MM-DD HH:MM:SS" (UTC) for every row, captures
    // included, so a plain string comparison orders it by time.
    case 'date': return doc.created_at || ''
    default: return ''
  }
}

// compareDocuments orders two rows by key, ascending. Ties fall back to the
// name and then the id, so the order is stable and never depends on the
// order the backend happened to return.
export function compareDocuments(a, b, key) {
  const primary = key === 'date'
    ? sortValue(a, key).localeCompare(sortValue(b, key))
    : collator.compare(sortValue(a, key), sortValue(b, key))
  if (primary !== 0) return primary
  const byName = collator.compare(a.filename || '', b.filename || '')
  if (byName !== 0) return byName
  return (a.id || '').localeCompare(b.id || '')
}

export function sortDocuments(docs, sort) {
  const { key, dir } = normalizeSort(sort)
  const sign = dir === 'desc' ? -1 : 1
  return [...docs].sort((a, b) => sign * compareDocuments(a, b, key))
}

export function normalizeSort(sort) {
  const key = SORT_KEYS.includes(sort?.key) ? sort.key : DEFAULT_SORT.key
  const dir = sort?.dir === 'asc' || sort?.dir === 'desc' ? sort.dir : DEFAULT_SORT.dir
  return { key, dir }
}

// nextSort is what clicking a column header does: the same column flips
// direction; a new column starts newest-first for dates and A→Z otherwise.
export function nextSort(current, key) {
  const cur = normalizeSort(current)
  if (cur.key === key) return { key, dir: cur.dir === 'asc' ? 'desc' : 'asc' }
  return { key, dir: key === 'date' ? 'desc' : 'asc' }
}

export function loadSort(storage = globalThis.localStorage) {
  try {
    const raw = storage?.getItem(SORT_STORAGE_KEY)
    return raw ? normalizeSort(JSON.parse(raw)) : { ...DEFAULT_SORT }
  } catch {
    return { ...DEFAULT_SORT }
  }
}

export function saveSort(sort, storage = globalThis.localStorage) {
  try {
    storage?.setItem(SORT_STORAGE_KEY, JSON.stringify(normalizeSort(sort)))
  } catch {
    // Storage can be unavailable (private mode, blocked site data); the
    // sort still applies for this session.
  }
}

// sourceOptions lists the distinct sources present, for the filter menu.
export function sourceOptions(docs) {
  return [...new Set(docs.map(d => d.source).filter(Boolean))]
    .sort((a, b) => collator.compare(sourceLabel(a), sourceLabel(b)))
}

export function filterBySource(docs, source) {
  return source ? docs.filter(d => d.source === source) : docs
}

// tagOptions lists the tags in use across docs, one entry per tag (by
// case-insensitive name), ordered by name.
export function tagOptions(docs) {
  const byName = new Map()
  for (const d of docs) {
    for (const t of d.tags || []) {
      if (!byName.has(t.name.toLowerCase())) byName.set(t.name.toLowerCase(), t)
    }
  }
  return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }))
}

// filterByTags keeps documents carrying every one of the named tags
// (case-insensitive); no names keeps everything.
export function filterByTags(docs, names) {
  if (!names || names.length === 0) return docs
  const want = names.map(n => n.toLowerCase())
  return docs.filter(d => {
    const have = new Set((d.tags || []).map(t => t.name.toLowerCase()))
    return want.every(n => have.has(n))
  })
}

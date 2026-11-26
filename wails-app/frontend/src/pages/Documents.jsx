import { useState, useEffect, useCallback, useRef, useMemo } from 'react'
import { Upload, Search, Trash2, Plus, X, Sparkles, CheckCircle2, AlertTriangle, XCircle, Eye, PlayCircle, ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import i18n from '../i18n.js'
import * as WailsApp from '../wailsjs/go/main/App'
import { confirm } from '../components/ConfirmDialog.jsx'
import { api, notify, onMonomindInitEvent, onDocumentsChanged } from '../services/api.js'
import FileViewerModal, { fileViewerKind } from '../components/FileViewerModal.jsx'
import CaptureViewerModal, { summaryStatusText } from '../components/CaptureViewerModal.jsx'
import { isMonomindNotFound } from '../lib/agentRuntimes.js'
import SortableTh from '../components/SortableTh.jsx'
import {
  sourceLabel, documentType, viewerFilename, isCapture, sortDocuments, nextSort,
  loadSort, saveSort, sourceOptions, filterBySource, tagOptions, filterByTags,
} from '../lib/documentSort.js'
import { useReloadOnActivate } from '../lib/useReloadOnActivate'

// maxInlinePreviewBytes mirrors the backend's own GetProfileDocumentData
// cap (wails-app/app_files.go) so an oversized file is routed straight to
// OpenPathWithOS instead of round-tripping to Go just to be rejected.
const maxInlinePreviewBytes = 25 * 1024 * 1024

// documentBadgeState classifies a document row into exactly one of four
// Knowledge Graph badge states. The 4th state (monomind_not_set_up) only
// overrides a NEVER-indexed row: a document that was successfully indexed
// before keeps showing indexed/stale even if monomind later becomes
// un-initialized for this profile, since that fact doesn't retroactively
// change what already happened.
export function documentBadgeState(doc, monomindNotInitialized) {
  if (!doc.indexed && monomindNotInitialized) return 'monomind_not_set_up'
  if (doc.indexed && doc.stale) return 'stale'
  if (doc.indexed) return 'indexed'
  return 'not_indexed'
}

export function formatBytes(n) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

const btnStyle = {
  background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
  borderRadius: 6, padding: '6px 12px', color: '#00b4d8',
  fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
  display: 'flex', alignItems: 'center', gap: 5,
}
const inputStyle = {
  background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
  padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
}

// Not indexed for the survivable reasons (never attempted, or a transient
// backend hiccup) vs. because monomind genuinely isn't installed at all --
// mirrors the fallback-copy pattern in Agents.jsx ("/not found/i.test(...)")
// against internal/monomind/find.go's ErrNotFound message.
export function isMonomindMissing(indexError) {
  return isMonomindNotFound(indexError)
}

function TagChip({ tag, onClick, onRemove, active }) {
  const { t } = useTranslation()
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', gap: 3,
      padding: '2px 6px', borderRadius: 4,
      background: `${tag.color}${active ? '40' : '1a'}`,
      border: `1px solid ${tag.color}${active ? 'aa' : '55'}`,
      color: tag.color, fontSize: 10, fontFamily: 'var(--font-mono)',
      whiteSpace: 'nowrap', lineHeight: 1.6,
    }}>
      <span
        role={onClick ? 'button' : undefined}
        title={onClick ? t('ui6PagesDocs.filter_by_name', { name: tag.name }) : undefined}
        style={{ cursor: onClick ? 'pointer' : 'default', maxWidth: 110, overflow: 'hidden', textOverflow: 'ellipsis' }}
        onClick={onClick && ((e) => { e.stopPropagation(); onClick() })}
      >{tag.name}</span>
      {onRemove && (
        <button
          type="button"
          aria-label={t('ui6PagesDocs.remove_tag_named', { name: tag.name })}
          title={t('ui6PagesDocs.remove_tag')}
          style={{ background: 'none', border: 'none', color: 'inherit', cursor: 'pointer', padding: 0, display: 'flex' }}
          onClick={(e) => { e.stopPropagation(); onRemove() }}
        ><X size={10} /></button>
      )}
    </span>
  )
}

// Inline "+ tag" box: type a name (existing tags are suggested), Enter adds.
function AddTagBox({ doc, knownTags, onAdd }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const listId = `doc-tags-${doc.id}`
  // Enter, Escape and the blur they cause all end the box; only the first counts.
  const done = useRef(false)
  const close = (add) => {
    if (done.current) return
    done.current = true
    const n = add ? name.trim() : ''
    setName('')
    setOpen(false)
    if (n) onAdd(doc.id, n)
  }
  const submit = () => close(true)
  if (!open) {
    return (
      <button
        type="button"
        aria-label={t('ui6PagesDocs.add_tag_to', { file: doc.filename })}
        title={t('ui6PagesDocs.add_tag')}
        style={{ ...btnStyle, padding: '1px 6px' }}
        onClick={(e) => { e.stopPropagation(); done.current = false; setOpen(true) }}
      ><Plus size={10} /></button>
    )
  }
  return (
    <>
      <input
        autoFocus
        aria-label={t('ui6PagesDocs.new_tag_for', { file: doc.filename })}
        list={listId}
        style={{ ...inputStyle, width: 110, padding: '2px 6px', fontSize: 10 }}
        placeholder={t('ui6PagesDocs.tag_name')}
        value={name}
        onClick={(e) => e.stopPropagation()}
        onChange={e => setName(e.target.value)}
        onKeyDown={e => {
          if (e.key === 'Enter') { e.preventDefault(); submit() }
          if (e.key === 'Escape') close(false)
        }}
        onBlur={submit}
      />
      <datalist id={listId}>
        {knownTags.filter(t => !(doc.tags || []).some(x => x.id === t.id)).map(t => <option key={t.id} value={t.name} />)}
      </datalist>
    </>
  )
}

export default function Documents({ isActive = true }) {
  const { t } = useTranslation()
  const [docs, setDocs] = useState([])
  const [error, setError] = useState(null)
  const [query, setQuery] = useState('')
  const [results, setResults] = useState(null)
  const [searching, setSearching] = useState(false)
  // Per-profile check, independent of any single document's own
  // indexed/index_error outcome -- lets us suggest setup BEFORE the user
  // uploads anything, not just after a failed upload. Mirrors Agents.jsx's
  // identical notInitialized check.
  const [notInitialized, setNotInitialized] = useState(false)
  const [initStatus, setInitStatus] = useState('idle') // idle | running | error
  const initRunningRef = useRef(false)
  const [viewingDoc, setViewingDoc] = useState(null)
  const [indexingIds, setIndexingIds] = useState(() => new Set())
  const [selectedIds, setSelectedIds] = useState(() => new Set())
  // null | 'index' | 'delete' — gates re-entrancy on the bulk buttons (and
  // the per-row Index button, to avoid a race with an in-flight bulk run)
  // and drives the running bulk button's live progress label.
  const [bulkBusy, setBulkBusy] = useState(null)
  const [bulkProgress, setBulkProgress] = useState({ done: 0, total: 0 })
  const [sort, setSort] = useState(() => loadSort())
  const [sourceFilter, setSourceFilter] = useState('')
  const [tagFilter, setTagFilter] = useState([]) // tag names; a document must carry all

  const onSort = (key) => setSort(cur => {
    const next = nextSort(cur, key)
    saveSort(next)
    return next
  })
  // What the table shows: filtered by source, then sorted. Selection and
  // select-all work on this list; "Index all" still covers every document.
  const visibleDocs = useMemo(
    () => sortDocuments(filterByTags(filterBySource(docs, sourceFilter), tagFilter), sort),
    [docs, sourceFilter, tagFilter, sort],
  )
  const sources = useMemo(() => sourceOptions(docs), [docs])
  const knownTags = useMemo(() => tagOptions(docs), [docs])
  const toggleTagFilter = (name) => setTagFilter(cur => {
    const has = cur.some(n => n.toLowerCase() === name.toLowerCase())
    return has ? cur.filter(n => n.toLowerCase() !== name.toLowerCase()) : [...cur, name]
  })
  // A filter on a tag no document carries any more would hide everything
  // with no way to see why; drop it.
  useEffect(() => {
    setTagFilter(cur => {
      const live = cur.filter(n => knownTags.some(t => t.name.toLowerCase() === n.toLowerCase()))
      return live.length === cur.length ? cur : live
    })
  }, [knownTags])

  const handleAddTag = async (docId, name) => {
    try {
      const tag = await WailsApp.AddDocumentTag(docId, name)
      if (!tag) { notify(i18n.t('ui6PagesDocs.op_tag'), i18n.t('ui6PagesDocs.tag_refused', { name })); return }
      setDocs(cur => cur.map(d => d.id !== docId || (d.tags || []).some(t => t.id === tag.id)
        ? d
        : { ...d, tags: [...(d.tags || []), tag].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })) }))
    } catch (e) {
      notify(i18n.t('ui6PagesDocs.op_tag'), i18n.t('ui6PagesDocs.tag_failed', { error: String(e) }))
    }
  }
  const handleRemoveTag = async (docId, tagId) => {
    try {
      await WailsApp.RemoveDocumentTag(docId, tagId)
      setDocs(cur => cur.map(d => d.id !== docId ? d : { ...d, tags: (d.tags || []).filter(t => t.id !== tagId) }))
    } catch (e) {
      notify(i18n.t('ui6PagesDocs.op_tag'), i18n.t('ui6PagesDocs.remove_tag_failed', { error: String(e) }))
    }
  }

  const load = useCallback(async () => {
    try {
      setDocs((await WailsApp.ListProfileDocuments()) || [])
    } catch (e) {
      setError(i18n.t('ui6PagesDocs.load_failed', { error: String(e) }))
    }
  }, [])

  useEffect(() => { load() }, [load])
  useReloadOnActivate(isActive, load)
  useEffect(() => { api.isMonomindInitialized().then(v => setNotInitialized(!v)) }, [])

  // Live-refresh: the background document watcher (wails-app/app_documents_watch.go)
  // discovers files added to the profile folder from outside the app (e.g.
  // dropped in via Finder) and reconciles them into the same list this
  // page reads — reload whenever it signals a change.
  useEffect(() => onDocumentsChanged(() => load()), [load])

  useEffect(() => onMonomindInitEvent((payload) => {
    if (!initRunningRef.current) return
    if (payload.kind === 'error') {
      initRunningRef.current = false
      setInitStatus('error')
      notify(i18n.t('ui6PagesDocs.op_init'), payload.message || i18n.t('ui6PagesDocs.failed'))
    } else if (payload.kind === 'done') {
      initRunningRef.current = false
      setInitStatus('idle')
      setNotInitialized(false)
    }
  }), [])

  const startMonomindInit = async () => {
    initRunningRef.current = true
    setInitStatus('running')
    // The binding returns before init finishes; progress arrives as events.
    // A refusal (e.g. signed out) is returned without an event, so read it
    // here or the spinner would never stop.
    let message = ''
    try {
      const r = await api.initializeMonomindProfile()
      if (r && r.error) message = r.login_required ? i18n.t('ui6PagesDocs.sign_in_retry', { error: r.error }) : r.error
    } catch (e) {
      message = e?.message || String(e)
    }
    if (message && initRunningRef.current) {
      initRunningRef.current = false
      setInitStatus('error')
      notify(i18n.t('ui6PagesDocs.op_init'), message)
    }
  }

  const [uploading, setUploading] = useState(false)
  const uploadPaths = async (paths) => {
    if (!paths || paths.length === 0 || uploading) return
    setUploading(true)
    try {
      const r = await WailsApp.UploadProfileDocuments(paths)
      if (r.failed.length) notify(i18n.t('ui6PagesDocs.op_upload'), i18n.t('ui6PagesDocs.uploads_failed', { failed: r.failed.length, total: paths.length, details: r.failed.slice(0, 3).join('; ') }))
      else if (r.unindexed) notify(i18n.t('ui6PagesDocs.op_upload'), i18n.t('ui6PagesDocs.uploaded_unindexed', { n: r.unindexed }))
    } catch (e) {
      notify(i18n.t('ui6PagesDocs.op_upload'), i18n.t('ui6PagesDocs.upload_failed', { error: String(e) }))
    } finally {
      setUploading(false)
      load()
    }
  }

  const handleUpload = async () => uploadPaths(await WailsApp.OpenAnyFilesPicker(i18n.t('ui6PagesDocs.pick_documents')))
  const handleUploadFolder = async () => {
    let paths
    try {
      paths = await WailsApp.OpenFolderFilesPicker(i18n.t('ui6PagesDocs.pick_folder'))
    } catch (e) {
      notify(i18n.t('ui6PagesDocs.op_upload'), String(e)) // e.g. another dialog is still open: answer or close it first
      return
    }
    return uploadPaths(paths)
  }

  // Returns whether indexing succeeded, so bulk callers (runBulkIndex) can
  // tally failures for their own summary notify() without duplicating this
  // function's own per-item notify/spinner/reload handling.
  const handleIndex = async (id) => {
    setIndexingIds(s => new Set(s).add(id))
    try {
      const result = await WailsApp.IndexProfileDocument(id)
      if (!result.indexed) {
        notify(i18n.t('ui6PagesDocs.op_index'), isMonomindMissing(result.index_error)
          ? i18n.t('ui6PagesDocs.not_indexed_no_monomind')
          : i18n.t('ui6PagesDocs.indexing_failed', { error: String(result.index_error) }))
      }
      load()
      return result.indexed
    } catch (e) {
      notify(i18n.t('ui6PagesDocs.op_index'), i18n.t('ui6PagesDocs.indexing_failed', { error: String(e) }))
      return false
    } finally {
      setIndexingIds(s => { const n = new Set(s); n.delete(id); return n })
    }
  }

  const toggleSelected = (id) => {
    setSelectedIds(s => {
      const n = new Set(s)
      if (n.has(id)) n.delete(id); else n.add(id)
      return n
    })
  }

  const allVisibleSelected = visibleDocs.length > 0 && visibleDocs.every(d => selectedIds.has(d.id))
  const toggleSelectAll = () => {
    setSelectedIds(allVisibleSelected ? new Set() : new Set(visibleDocs.map(d => d.id)))
  }

  // Shared by the always-available "Index all" button and the
  // selection-scoped "Index selected" action -- both filter to the exact
  // predicate documentBadgeState/the per-row Index button already use
  // (!indexed || stale), so neither ever redundantly re-indexes an
  // already-fresh document. Reuses handleIndex per-item, so bulk runs get
  // the same live per-row spinner and per-item error toast it already has,
  // for free.
  const runBulkIndex = async (candidateDocs) => {
    const toIndex = candidateDocs.filter(d => !d.indexed || d.stale)
    if (toIndex.length === 0) {
      notify(i18n.t('ui6PagesDocs.op_index'), i18n.t('ui6PagesDocs.nothing_to_index'))
      return
    }
    setBulkBusy('index')
    setBulkProgress({ done: 0, total: toIndex.length })
    let failed = 0
    for (const d of toIndex) {
      const ok = await handleIndex(d.id)
      if (!ok) failed++
      setBulkProgress(p => ({ ...p, done: p.done + 1 }))
    }
    notify(i18n.t('ui6PagesDocs.op_index'), failed > 0
      ? i18n.t('ui6PagesDocs.indexed_partial', { count: toIndex.length, done: toIndex.length - failed, failed })
      : i18n.t('ui6PagesDocs.indexed_done', { count: toIndex.length }))
    setBulkBusy(null)
  }

  // Deleting an upload deletes MonoAgent's copy. Deleting an auto-discovered
  // ("Profile folder") document removes it from MonoAgent -- index and
  // knowledge graph -- and excludes it so the folder scan does not add it
  // back; its file stays on disk unless the user ticks the box below.
  const runBulkDelete = async (toDelete) => {
    if (toDelete.length === 0) return
    const discovered = toDelete.filter(d => d.source === 'discovered')
    const question = toDelete.length === 1
      ? i18n.t('ui6PagesDocs.confirm_delete_one', { name: toDelete[0].filename })
      : i18n.t('ui6PagesDocs.confirm_delete_many', { n: toDelete.length })
    const captures = toDelete.filter(isCapture).length
    const captureNote = captures > 0 ? ` ${i18n.t('ui6PagesDocs.capture_note')}` : ''
    const discoveredNote = discovered.length > 0
      ? ` ${discovered.length === toDelete.length
        ? i18n.t('ui6PagesDocs.discovered_all', { count: discovered.length })
        : i18n.t('ui6PagesDocs.discovered_some', { count: discovered.length })}`
      : ''
    const answer = await confirm(
      `${question}${captureNote}${discoveredNote}`,
      {
        title: i18n.t('ui6PagesDocs.delete_documents_title'), confirmLabel: i18n.t('ui6PagesDocs.delete'), danger: true,
        ...(discovered.length > 0 && {
          checkbox: {
            label: i18n.t('ui6PagesDocs.delete_file_label'),
            warning: i18n.t('ui6PagesDocs.delete_file_warning'),
          },
        }),
      },
    )
    if (!answer) return
    const deleteFile = !!answer.checked

    setBulkBusy('delete')
    setBulkProgress({ done: 0, total: toDelete.length })
    let failed = 0
    const deletedIds = new Set()
    for (const d of toDelete) {
      try {
        if (d.source === 'discovered') await WailsApp.RemoveProfileDocument(d.id, deleteFile)
        else await WailsApp.DeleteProfileDocument(d.id)
        deletedIds.add(d.id)
      } catch (e) {
        failed++
        notify(i18n.t('ui6PagesDocs.op_delete'), i18n.t('ui6PagesDocs.delete_one_failed', { name: d.filename, error: String(e) }))
      }
      setBulkProgress(p => ({ ...p, done: p.done + 1 }))
    }
    load()
    loadExcluded()
    setSelectedIds(s => { const n = new Set(s); deletedIds.forEach(id => n.delete(id)); return n })
    if (failed === 0) {
      notify(i18n.t('ui6PagesDocs.op_delete'), i18n.t('ui6PagesDocs.deleted_done', { count: toDelete.length }))
    }
    setBulkBusy(null)
  }

  // "Show excluded": auto-discovered documents removed from MonoAgent.
  const [showExcluded, setShowExcluded] = useState(false)
  const [excluded, setExcluded] = useState([])
  const loadExcluded = useCallback(async () => {
    try { setExcluded((await WailsApp.ListExcludedDocuments()) || []) } catch { setExcluded([]) }
  }, [])
  useEffect(() => { loadExcluded() }, [loadExcluded])
  const handleRestore = async (item) => {
    try {
      await WailsApp.RestoreExcludedDocument(item.path)
      await loadExcluded()
      load()
    } catch (e) {
      notify(i18n.t('ui6PagesDocs.op_restore'), i18n.t('ui6PagesDocs.restore_failed', { error: String(e) }))
    }
  }

  const handleDelete = async (id, filename, capture = false) => {
    const note = capture ? ` ${i18n.t('ui6PagesDocs.saved_page_note')}` : ''
    if (!(await confirm(`${i18n.t('ui6PagesDocs.confirm_delete_one', { name: filename })}${note}`, { title: i18n.t('ui6PagesDocs.delete_document_title'), confirmLabel: i18n.t('ui6PagesDocs.delete'), danger: true }))) return
    try {
      await WailsApp.DeleteProfileDocument(id)
      load()
    } catch (e) {
      notify(i18n.t('ui6PagesDocs.op_delete'), i18n.t('ui6PagesDocs.delete_failed', { error: String(e) }))
    }
  }

  // Double-click / View: preview in-app when a viewer exists for this file's
  // extension; otherwise tell the user and hand the file to the OS's own
  // default application, the same as double-clicking it in a file manager.
  // A browser capture previews as its primary artifact (readable.md in-app;
  // an MHTML archive goes to the OS, which opens it in a browser).
  const handleOpenDocument = (d) => {
    if (isCapture(d)) return setViewingDoc({ ...d, capture: true })
    const name = viewerFilename(d)
    if (fileViewerKind(name) && d.size_bytes <= maxInlinePreviewBytes) {
      setViewingDoc({ ...d, filename: name })
      return
    }
    const type = documentType(d).toUpperCase()
    const name2 = d.filename
    notify(i18n.t('ui6PagesDocs.op_open'), d.size_bytes > maxInlinePreviewBytes
      ? i18n.t('ui6PagesDocs.too_large', { name: name2 })
      : type ? i18n.t('ui6PagesDocs.no_viewer', { type, name: name2 }) : i18n.t('ui6PagesDocs.no_viewer_unknown', { name: name2 }))
    WailsApp.OpenPathWithOS(d.path).catch(e => notify(i18n.t('ui6PagesDocs.op_open'), i18n.t('ui6PagesDocs.open_failed', { name: d.filename, error: String(e) })))
  }

  const handleSearch = async (e) => {
    e.preventDefault()
    if (!query.trim()) return
    setSearching(true)
    setError(null)
    try {
      setResults(await WailsApp.SearchProfileKnowledge(query))
    } catch (e) {
      setError(i18n.t('ui6PagesDocs.search_failed', { error: String(e) }))
    } finally {
      setSearching(false)
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', padding: 16, gap: 12, overflow: 'hidden' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <h2 style={{ margin: 0, fontSize: 16, color: 'var(--text-primary)' }}>{t('ui6PagesDocs.title')}</h2>
        <div style={{ flex: 1 }} />
        {selectedIds.size > 0 && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>{t('ui6PagesDocs.n_selected', { n: selectedIds.size })}</span>
            <button
              style={btnStyle}
              disabled={bulkBusy !== null}
              onClick={() => runBulkIndex(docs.filter(d => selectedIds.has(d.id)))}
            >
              <PlayCircle size={13} /> {bulkBusy === 'index' ? t('ui6PagesDocs.indexing_progress', { done: bulkProgress.done, total: bulkProgress.total }) : t('ui6PagesDocs.index_selected', { n: selectedIds.size })}
            </button>
            <button
              style={{ ...btnStyle, color: '#ef4444', border: '1px solid rgba(239,68,68,0.3)' }}
              disabled={bulkBusy !== null}
              onClick={() => runBulkDelete(docs.filter(d => selectedIds.has(d.id)))}
            >
              <Trash2 size={13} /> {bulkBusy === 'delete' ? t('ui6PagesDocs.deleting_progress', { done: bulkProgress.done, total: bulkProgress.total }) : t('ui6PagesDocs.delete_selected', { n: selectedIds.size })}
            </button>
          </div>
        )}
        <button style={btnStyle} disabled={bulkBusy !== null} onClick={() => runBulkIndex(docs)}>
          <PlayCircle size={13} /> {bulkBusy === 'index' ? t('ui6PagesDocs.indexing_progress', { done: bulkProgress.done, total: bulkProgress.total }) : t('ui6PagesDocs.index_all')}
        </button>
        <button style={btnStyle} disabled={uploading} onClick={handleUpload}><Upload size={13} /> {uploading ? t('ui6PagesDocs.uploading') : t('ui6PagesDocs.upload')}</button>
        <button style={btnStyle} disabled={uploading} onClick={handleUploadFolder}><Upload size={13} /> {t('ui6PagesDocs.upload_folder')}</button>
      </div>

      <form onSubmit={handleSearch} style={{ display: 'flex', gap: 8 }}>
        <input style={{ ...inputStyle, flex: 1 }} placeholder={t('ui6PagesDocs.search_placeholder')} value={query} onChange={e => setQuery(e.target.value)} />
        <button type="submit" style={btnStyle} disabled={searching}><Search size={13} /> {t('ui6PagesDocs.search')}</button>
        <select
          aria-label={t('ui6PagesDocs.filter_source')}
          className="filter-select"
          style={{ fontFamily: 'var(--font-mono)', fontSize: 11, colorScheme: 'dark' }}
          value={sourceFilter}
          onChange={e => setSourceFilter(e.target.value)}
        >
          <option value="">{t('ui6PagesDocs.all_sources')}</option>
          {sources.map(src => <option key={src} value={src}>{sourceLabel(src)}</option>)}
        </select>
      </form>

      {knownTags.length > 0 && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }} aria-label={t('ui6PagesDocs.filter_tag')}>
          <span style={{ fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase' }}>{t('ui6PagesDocs.tags')}</span>
          {knownTags.map(t => (
            <TagChip
              key={t.id}
              tag={t}
              active={tagFilter.some(n => n.toLowerCase() === t.name.toLowerCase())}
              onClick={() => toggleTagFilter(t.name)}
            />
          ))}
          {tagFilter.length > 0 && (
            <button type="button" style={{ ...btnStyle, padding: '1px 8px' }} onClick={() => setTagFilter([])}>{t('ui6PagesDocs.clear')}</button>
          )}
        </div>
      )}

      {error && <div style={{ color: '#ff6b6b', fontSize: 12 }}>{error}</div>}

      {notInitialized && (
        <div style={{
          display: 'flex', alignItems: 'center', gap: 10, padding: '8px 12px',
          background: 'rgba(245,158,11,0.08)', border: '1px solid rgba(245,158,11,0.25)', borderRadius: 6,
        }}>
          <Sparkles size={14} color="#f59e0b" style={{ flexShrink: 0 }} />
          <div style={{ fontSize: 11, color: 'var(--text-secondary)', flex: 1 }}>
            {initStatus === 'running'
              ? t('ui6PagesDocs.init_running')
              : t('ui6PagesDocs.init_needed')}
          </div>
          {initStatus === 'running' ? (
            <div className="spinner" style={{ width: 14, height: 14 }} />
          ) : (
            <button style={{ ...btnStyle, padding: '4px 10px', flexShrink: 0 }} onClick={startMonomindInit}>{t('ui6PagesDocs.init_button')}</button>
          )}
        </div>
      )}

      {results && (
        <div style={{ maxHeight: '30%', overflow: 'auto', border: '1px solid rgba(255,255,255,0.08)', borderRadius: 6, padding: 8 }}>
          {results.length === 0 && <div style={{ color: 'var(--text-muted)', fontSize: 12 }}>{t('ui6PagesDocs.no_results')}</div>}
          {results.map((r, i) => (
            <div key={i} style={{ fontSize: 11, padding: '4px 0', borderBottom: i < results.length - 1 ? '1px solid rgba(255,255,255,0.05)' : 'none' }}>
              <span style={{ color: '#00b4d8' }}>[{r.score.toFixed(2)}]</span> <span style={{ color: 'var(--text-muted)' }}>{r.path}</span>
              <div>{r.excerpt}</div>
            </div>
          ))}
        </div>
      )}

      <div style={{ flex: 1, overflow: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }}>
          <thead>
            <tr style={{ textAlign: 'left', color: 'var(--text-muted)', fontSize: 10, textTransform: 'uppercase' }}>
              <th style={{ padding: '6px 8px' }}>
                <input
                  type="checkbox"
                  aria-label={t('ui6PagesDocs.select_all')}
                  style={{ accentColor: '#00b4d8', width: 14, height: 14 }}
                  checked={allVisibleSelected}
                  onChange={toggleSelectAll}
                />
              </th>
              <SortableTh label={t('ui6PagesDocs.col_name')} sortKey="name" sort={sort} onSort={onSort} />
              <SortableTh label={t('ui6PagesDocs.col_type')} sortKey="type" sort={sort} onSort={onSort} />
              <th style={{ padding: '6px 8px' }}>{t('ui6PagesDocs.tags')}</th>
              <SortableTh label={t('ui6PagesDocs.col_source')} sortKey="source" sort={sort} onSort={onSort} />
              <th style={{ padding: '6px 8px' }}>{t('ui6PagesDocs.col_size')}</th>
              <SortableTh label={t('ui6PagesDocs.col_date')} sortKey="date" sort={sort} onSort={onSort} />
              <th style={{ padding: '6px 8px' }}>{t('ui6PagesDocs.col_graph')}</th>
              <th style={{ padding: '6px 8px' }} />
            </tr>
          </thead>
          <tbody>
            {visibleDocs.map(d => (
              <tr
                key={d.id}
                onDoubleClick={() => handleOpenDocument(d)}
                style={{ borderTop: '1px solid rgba(255,255,255,0.05)', cursor: 'pointer' }}
              >
                <td style={{ padding: '8px' }}>
                  <input
                    type="checkbox"
                    aria-label={t('ui6PagesDocs.select_file', { file: d.filename })}
                    style={{ accentColor: '#00b4d8', width: 14, height: 14 }}
                    checked={selectedIds.has(d.id)}
                    onClick={(e) => e.stopPropagation()}
                    onChange={() => toggleSelected(d.id)}
                  />
                </td>
                <td style={{ padding: '8px' }} title={t('ui6PagesDocs.double_click')}>
                  <div>{d.filename}</div>
                  {d.summary_status && (
                    <div
                      data-testid="summary-status"
                      title={summaryStatusText({ status: d.summary_status })}
                      style={{ fontSize: 10, color: ['error', 'stalled'].includes(d.summary_status) ? '#f87171' : d.summary_status === 'done' ? '#10b981' : '#94a3b8' }}
                    >
                      {{ done: t('ui6PagesDocs.summary_done'), pending: t('ui6PagesDocs.summary_pending'), running: t('ui6PagesDocs.summary_running'), error: t('ui6PagesDocs.summary_error'), stalled: t('ui6PagesDocs.summary_stalled') }[d.summary_status] || d.summary_status}
                    </div>
                  )}
                  {d.url && (
                    <div style={{ fontSize: 10, color: 'var(--text-muted)', maxWidth: 360, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={d.url}>
                      {d.url}
                    </div>
                  )}
                </td>
                <td style={{ padding: '8px', color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', fontSize: 10 }}>{documentType(d).toUpperCase() || '—'}</td>
                <td style={{ padding: '8px' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap' }}>
                    {(d.tags || []).map(t => (
                      <TagChip key={t.id} tag={t} onClick={() => toggleTagFilter(t.name)} onRemove={() => handleRemoveTag(d.id, t.id)} />
                    ))}
                    {(d.tags || []).length < 10 && <AddTagBox doc={d} knownTags={knownTags} onAdd={handleAddTag} />}
                  </div>
                </td>
                <td style={{ padding: '8px', color: 'var(--text-muted)' }} title={d.source}>{sourceLabel(d.source)}</td>
                <td style={{ padding: '8px', color: 'var(--text-muted)' }}>{formatBytes(d.size_bytes)}</td>
                <td style={{ padding: '8px', color: 'var(--text-muted)' }}>{d.created_at}</td>
                <td style={{ padding: '8px' }}>
                  {(() => {
                    const state = documentBadgeState(d, notInitialized)
                    if (state === 'indexed') {
                      return (
                        <span style={{ display: 'flex', alignItems: 'center', gap: 4, color: '#10b981', fontSize: 10 }}>
                          <CheckCircle2 size={12} /> {t('ui6PagesDocs.indexed')}
                        </span>
                      )
                    }
                    if (state === 'stale') {
                      return (
                        <span
                          title={d.index_error || t('ui6PagesDocs.stale_title')}
                          style={{ display: 'flex', alignItems: 'center', gap: 4, color: '#f59e0b', fontSize: 10, cursor: 'help' }}
                        >
                          <AlertTriangle size={12} /> {t('ui6PagesDocs.stale')}
                        </span>
                      )
                    }
                    if (state === 'monomind_not_set_up') {
                      return (
                        <span style={{ display: 'flex', alignItems: 'center', gap: 4, color: '#f59e0b', fontSize: 10 }}>
                          <Sparkles size={12} /> {t('ui6PagesDocs.monomind_not_set_up')}
                        </span>
                      )
                    }
                    return (
                      <span
                        title={d.index_error || t('ui6PagesDocs.not_yet_indexed')}
                        style={{ display: 'flex', alignItems: 'center', gap: 4, color: '#64748b', fontSize: 10, cursor: d.index_error ? 'help' : 'default' }}
                      >
                        <XCircle size={12} /> {isMonomindMissing(d.index_error) ? t('ui6PagesDocs.monomind_not_installed') : t('ui6PagesDocs.not_indexed')}
                      </span>
                    )
                  })()}
                </td>
                <td style={{ padding: '8px', display: 'flex', gap: 6 }}>
                  <button style={{ ...btnStyle, padding: '4px 8px' }} title={t('ui6PagesDocs.view')} onClick={(e) => { e.stopPropagation(); handleOpenDocument(d) }}>
                    <Eye size={12} />
                  </button>
                  {d.url && (
                    <button style={{ ...btnStyle, padding: '4px 8px' }} title={t('ui6PagesDocs.open_original_page')} onClick={(e) => { e.stopPropagation(); api.openURL(d.url) }}>
                      <ExternalLink size={12} />
                    </button>
                  )}
                  {(d.stale || !d.indexed) && (
                    <button
                      style={{ ...btnStyle, padding: '4px 8px' }}
                      title={d.stale ? t('ui6PagesDocs.reindex') : t('ui6PagesDocs.index')}
                      disabled={indexingIds.has(d.id) || bulkBusy !== null}
                      onClick={(e) => { e.stopPropagation(); handleIndex(d.id) }}
                    >
                      {indexingIds.has(d.id) ? <div className="spinner" style={{ width: 12, height: 12 }} /> : <PlayCircle size={12} />}
                    </button>
                  )}
                  <button style={{ ...btnStyle, color: '#ef4444', border: '1px solid rgba(239,68,68,0.3)', padding: '4px 8px' }} title={isCapture(d) ? t('ui6PagesDocs.delete_capture') : d.source === 'discovered' ? t('ui6PagesDocs.remove_from_app') : t('ui6PagesDocs.delete')} aria-label={t('ui6PagesDocs.delete_file_named', { file: d.filename })} onClick={(e) => { e.stopPropagation(); d.source === 'discovered' ? runBulkDelete([d]) : handleDelete(d.id, d.filename, isCapture(d)) }}>
                    <Trash2 size={12} />
                  </button>
                </td>
              </tr>
            ))}
            {visibleDocs.length === 0 && (
              <tr><td colSpan={9} style={{ padding: 16, textAlign: 'center', color: 'var(--text-muted)' }}>
                {docs.length === 0 ? t('ui6PagesDocs.empty') : t('ui6PagesDocs.empty_filtered')}
              </td></tr>
            )}
          </tbody>
        </table>
      </div>

      {excluded.length > 0 && (
        <div style={{ marginTop: 12, fontSize: 11, fontFamily: 'var(--font-mono)' }}>
          <button style={{ ...btnStyle, padding: '3px 8px' }} onClick={() => setShowExcluded(v => !v)}>
            {showExcluded ? t('ui6PagesDocs.hide_excluded') : t('ui6PagesDocs.show_excluded', { n: excluded.length })}
          </button>
          {showExcluded && (
            <ul aria-label={t('ui6PagesDocs.excluded_list')} style={{ listStyle: 'none', padding: 0, margin: '8px 0 0' }}>
              {excluded.map(item => (
                <li key={item.path} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '4px 0', color: 'var(--text-muted)' }}>
                  <span style={{ flex: 1 }} title={item.path}>{item.relative_path || item.filename}</span>
                  <button style={{ ...btnStyle, padding: '3px 8px' }} aria-label={t('ui6PagesDocs.restore_named', { name: item.relative_path || item.filename })} onClick={() => handleRestore(item)}>{t('ui6PagesDocs.restore')}</button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {viewingDoc?.capture && <CaptureViewerModal doc={viewingDoc} onClose={() => setViewingDoc(null)} />}
      {viewingDoc && !viewingDoc.capture && <FileViewerModal doc={viewingDoc} onClose={() => setViewingDoc(null)} />}
    </div>
  )
}

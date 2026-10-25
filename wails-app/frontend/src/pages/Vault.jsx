import { useState, useEffect, useCallback, useMemo } from 'react'
import { Plus, Trash2, KeyRound, Download, Upload, Copy, Search, ArrowUp, ArrowDown } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { confirm } from '../components/ConfirmDialog.jsx'
import KeyValueFields, { newRow, validateRows } from '../components/KeyValueFields.jsx'
import VaultItemModal from '../components/VaultItemModal.jsx'
import VaultAgentAccess from '../components/VaultAgentAccess.jsx'
import { useReloadOnActivate } from '../lib/useReloadOnActivate'

const fmtDate = (s) => {
  if (!s) return '—'
  const d = new Date(s.includes('T') ? s : s.replace(' ', 'T') + 'Z')
  if (isNaN(d)) return s
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

// updated_at/created_at come back as "2026-08-01 12:00:00" (space, no Z) or
// ISO — normalize the same way fmtDate does so sort-by-date and the
// displayed date always agree, and so an unparseable value sorts as
// "oldest" instead of throwing NaN comparisons off in random directions.
const parseDate = (s) => {
  if (!s) return 0
  const d = new Date(s.includes('T') ? s : s.replace(' ', 'T') + 'Z')
  return isNaN(d) ? 0 : d.getTime()
}

const KIND_COLORS = {
  login: { bg: 'rgba(0,180,216,0.1)', border: 'rgba(0,180,216,0.25)', color: '#00b4d8' },
  secret: { bg: 'rgba(124,58,237,0.15)', border: 'rgba(124,58,237,0.3)', color: '#a78bfa' },
  connection: { bg: 'rgba(16,185,129,0.12)', border: 'rgba(16,185,129,0.3)', color: '#10b981' },
  session: { bg: 'rgba(245,158,11,0.12)', border: 'rgba(245,158,11,0.3)', color: '#f59e0b' },
  ai_provider: { bg: 'rgba(236,72,153,0.12)', border: 'rgba(236,72,153,0.3)', color: '#ec4899' },
}
const KIND_LABELS = {
  secret: 'keys',
  login: 'login',
  connection: 'connection',
  session: 'session',
  ai_provider: 'ai key',
}

// Pure so it's directly unit-testable (see Vault.test.js) without rendering
// the component — mirrors the codebase's existing pattern of exporting the
// filtering/validation logic out of KeyValueFields.jsx for the same reason.
export function filterAndSortEntries(entries, { search = '', sortBy = 'name', sortDir = 'asc' } = {}) {
  const q = search.trim().toLowerCase()
  const filtered = q
    ? entries.filter(e => (
        e.name?.toLowerCase().includes(q) ||
        e.username?.toLowerCase().includes(q) ||
        e.kind?.toLowerCase().includes(q) ||
        KIND_LABELS[e.kind]?.toLowerCase().includes(q)
      ))
    : entries
  const sorted = [...filtered].sort((a, b) => {
    const cmp = sortBy === 'date'
      ? parseDate(a.updated_at) - parseDate(b.updated_at)
      : (a.name || '').localeCompare(b.name || '')
    return sortDir === 'asc' ? cmp : -cmp
  })
  return sorted
}

const kindBadge = (kind) => {
  const s = KIND_COLORS[kind] || { bg: '#1a2332', border: '#334', color: '#64748b' }
  const label = KIND_LABELS[kind] || kind
  return (
    <span style={{
      background: s.bg, border: `1px solid ${s.border}`, borderRadius: 3,
      padding: '1px 6px', fontFamily: 'var(--font-mono)', fontSize: 9, color: s.color,
    }}>{label}</span>
  )
}

const inputStyle = {
  background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
  padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
}
const headerBtnStyle = {
  background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
  borderRadius: 6, padding: '6px 12px', color: '#00b4d8',
  fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
  display: 'flex', alignItems: 'center', gap: 5,
}

const emptyForm = () => ({ kind: 'secret', name: '', fields: [newRow('secret', '')], username: '', url: '', notes: '' })

// Clickable column header for the Name/Updated columns: shows an arrow for
// whichever column is the active sort, clicking flips direction (or picks
// this column, if it wasn't already the active one — see toggleSort).
function SortableHeader({ col, active, dir, onClick, children, style }) {
  const isActive = active === col
  const Icon = dir === 'asc' ? ArrowUp : ArrowDown
  return (
    <div
      onClick={() => onClick(col)}
      style={{
        ...style, display: 'flex', alignItems: 'center', gap: 3, cursor: 'pointer',
        color: isActive ? '#00b4d8' : undefined, userSelect: 'none',
      }}
    >
      {children}
      {isActive && <Icon size={10} />}
    </div>
  )
}

export default function Vault({ isActive = true }) {
  const [entries, setEntries] = useState([])
  const [showAdd, setShowAdd] = useState(false)
  const [form, setForm] = useState(emptyForm())
  const [error, setError] = useState(null)
  const [editingEntry, setEditingEntry] = useState(null)
  const [exportResult, setExportResult] = useState(null)
  const [importPath, setImportPath] = useState(null)
  const [importPassphrase, setImportPassphrase] = useState('')
  const [importResult, setImportResult] = useState(null)
  const [search, setSearch] = useState('')
  const [sortBy, setSortBy] = useState('name') // 'name' | 'date'
  const [sortDir, setSortDir] = useState('asc') // 'asc' | 'desc'
  // Decoupled from `error`: shown only by the import passphrase modal and
  // the import-complete modal, so an unrelated later failure (e.g. a
  // delete) can never surface inside an import dialog, and an import
  // failure never renders twice (once in the modal, once in the page
  // banner behind it).
  const [importError, setImportError] = useState(null)

  const load = useCallback(async () => {
    try {
      const list = await WailsApp.ListSecrets()
      setEntries(list || [])
    } catch (e) {
      setError('Failed to load vault: ' + e)
    }
  }, [])

  useEffect(() => { load() }, [load])
  useReloadOnActivate(isActive, load)

  // Toggling the same column flips direction; switching columns starts each
  // at its more useful default (name: A-Z, date: newest first).
  const toggleSort = (col) => {
    if (sortBy === col) {
      setSortDir(d => (d === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortBy(col)
      setSortDir(col === 'date' ? 'desc' : 'asc')
    }
  }

  const visibleEntries = useMemo(
    () => filterAndSortEntries(entries, { search, sortBy, sortDir }),
    [entries, search, sortBy, sortDir]
  )

  const handleAdd = async (e) => {
    e.preventDefault()
    setError(null)
    const { fields, error: rowsError } = validateRows(form.fields)
    if (rowsError) {
      setError(rowsError)
      return
    }
    if (Object.keys(fields).length === 0) {
      setError('At least one field is required.')
      return
    }
    try {
      await WailsApp.AddSecret(form.kind, form.name, form.username, form.url, form.notes, fields)
      setForm(emptyForm())
      setShowAdd(false)
      load()
    } catch (e) {
      setError('Save failed: ' + e)
    }
  }

  const handleDelete = async (name) => {
    if (!(await confirm('Delete this vault entry? This cannot be undone.', { title: 'Delete Vault Entry', confirmLabel: 'Delete' }))) return
    setError(null)
    try {
      await WailsApp.DeleteSecret(name)
      setEntries(prev => prev.filter(e => e.name !== name))
    } catch (e) {
      setError('Delete failed: ' + e)
    }
  }

  const handleExport = async () => {
    setError(null)
    try {
      const result = await WailsApp.ExportVaultAll()
      if (result.cancelled) return
      setExportResult(result)
    } catch (e) {
      setError('Export failed: ' + e)
    }
  }

  const handleImportPick = async () => {
    setError(null)
    setImportError(null)
    try {
      const path = await WailsApp.OpenVaultImportFilePicker()
      if (!path) return
      setImportPath(path)
      setImportPassphrase('')
    } catch (e) {
      setError('Import failed: ' + e)
    }
  }

  const handleImportSubmit = async (e) => {
    e.preventDefault()
    setImportError(null)
    try {
      const result = await WailsApp.ImportVaultAll(importPath, importPassphrase)
      setImportPath(null)
      setImportPassphrase('')
      setImportResult(result)
      load()
    } catch (e) {
      setImportError('Import failed: ' + e)
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      <div style={{
        padding: '14px 20px 10px', borderBottom: '1px solid #0d1a26',
        display: 'flex', alignItems: 'center', gap: 12,
      }}>
        <div>
          <div style={{ color: '#e2e8f0', fontSize: 16, fontWeight: 600 }}>Vault</div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569' }}>
            {visibleEntries.length === entries.length
              ? `${entries.length} ${entries.length === 1 ? 'entry' : 'entries'}`
              : `${visibleEntries.length} of ${entries.length} entries`}
          </div>
        </div>
        <div style={{ flex: 1 }} />
        <button onClick={handleImportPick} style={headerBtnStyle}>
          <Upload size={12} /> Import
        </button>
        <button onClick={handleExport} style={headerBtnStyle}>
          <Download size={12} /> Export All
        </button>
        <button onClick={() => setShowAdd(true)} style={headerBtnStyle}>
          <Plus size={12} /> Add New Item
        </button>
      </div>

      <VaultAgentAccess isActive={isActive} />

      <div style={{ padding: '10px 20px', display: 'flex', alignItems: 'center', gap: 8 }}>
        <div style={{ position: 'relative', display: 'flex', alignItems: 'center', flex: 1, maxWidth: 320 }}>
          <Search size={13} style={{ position: 'absolute', left: 10, color: '#475569', pointerEvents: 'none' }} />
          <input
            placeholder="Search name, username, or kind..."
            value={search}
            onChange={e => setSearch(e.target.value)}
            style={{ ...inputStyle, width: '100%', paddingLeft: 28 }}
          />
        </div>
      </div>

      {error && (
        <div style={{ margin: '8px 20px', background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 5, padding: '7px 10px', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>
          {error}
        </div>
      )}

      {showAdd && (
        <form
          onSubmit={handleAdd}
          style={{
            margin: '10px 20px', padding: 14, background: '#0d1a26',
            border: '1px solid #1e3a4f', borderRadius: 6,
            display: 'flex', flexDirection: 'column', gap: 8,
          }}
        >
          <div style={{ display: 'flex', gap: 8 }}>
            <select
              value={form.kind}
              onChange={e => setForm({ ...form, kind: e.target.value })}
              className="select-compact"
              style={{ flex: '0 0 auto', fontSize: 11 }}
            >
              <option value="secret">Keys</option>
              <option value="login">Login</option>
            </select>
            <input
              placeholder="Name"
              value={form.name}
              onChange={e => setForm({ ...form, name: e.target.value })}
              required
              style={{ ...inputStyle, flex: 1 }}
            />
          </div>
          {form.kind === 'login' && (
            <div style={{ display: 'flex', gap: 8 }}>
              <input
                placeholder="Username"
                value={form.username}
                onChange={e => setForm({ ...form, username: e.target.value })}
                style={{ ...inputStyle, flex: 1 }}
              />
              <input
                placeholder="URL"
                value={form.url}
                onChange={e => setForm({ ...form, url: e.target.value })}
                style={{ ...inputStyle, flex: 1 }}
              />
            </div>
          )}
          <KeyValueFields rows={form.fields} onChange={f => setForm({ ...form, fields: f })} />
          <textarea
            placeholder="Notes"
            value={form.notes}
            onChange={e => setForm({ ...form, notes: e.target.value })}
            rows={2}
            style={{ ...inputStyle, resize: 'vertical' }}
          />
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <button
              type="button"
              onClick={() => { setShowAdd(false); setForm(emptyForm()) }}
              style={{
                background: 'none', border: '1px solid #1e3a4f', borderRadius: 5,
                padding: '6px 12px', color: '#94a3b8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
              }}
            >
              Cancel
            </button>
            <button
              type="submit"
              style={{
                background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
                borderRadius: 5, padding: '6px 12px', color: '#00b4d8',
                fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
              }}
            >
              Save
            </button>
          </div>
        </form>
      )}

      <div style={{
        display: 'flex', alignItems: 'center', gap: 0,
        padding: '5px 20px', borderBottom: '1px solid #0a1520',
        fontFamily: 'var(--font-mono)', fontSize: 9, color: '#334155',
        letterSpacing: '1px', textTransform: 'uppercase',
      }}>
        <SortableHeader style={{ flex: 1 }} col="name" active={sortBy} dir={sortDir} onClick={toggleSort}>Name</SortableHeader>
        <div style={{ width: 70 }}>Kind</div>
        <div style={{ width: 110 }}>Username</div>
        <div style={{ width: 90 }}>Fields</div>
        <SortableHeader style={{ width: 56 }} col="date" active={sortBy} dir={sortDir} onClick={toggleSort}>Updated</SortableHeader>
        <div style={{ width: 28 }} />
      </div>

      <div style={{ flex: 1, overflowY: 'auto' }}>
        {entries.length === 0 && (
          <div style={{
            display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
            height: 200, gap: 12, color: '#334155',
          }}>
            <KeyRound size={32} style={{ opacity: 0.3 }} />
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>
              No vault entries yet — add a new item above
            </div>
          </div>
        )}
        {entries.length > 0 && visibleEntries.length === 0 && (
          <div style={{
            display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
            height: 200, gap: 12, color: '#334155',
          }}>
            <Search size={32} style={{ opacity: 0.3 }} />
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>
              No entries match "{search}"
            </div>
          </div>
        )}
        {visibleEntries.map(entry => (
          <div
            key={entry.id}
            onClick={() => setEditingEntry(entry)}
            style={{
              display: 'flex', alignItems: 'center', gap: 0, cursor: 'pointer',
              padding: '6px 20px', borderBottom: '1px solid #0a1520',
            }}
          >
            <div style={{ flex: 1, minWidth: 0, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#94a3b8', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', paddingRight: 10 }}>
              {entry.name}
            </div>
            <div style={{ width: 70 }}>{kindBadge(entry.kind)}</div>
            <div style={{ width: 110, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', paddingRight: 8 }}>
              {entry.username || '—'}
            </div>
            <div style={{ width: 90, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0' }}>
              {entry.field_count} {entry.field_count === 1 ? 'key' : 'keys'}
            </div>
            <div style={{ width: 56, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569' }}>{fmtDate(entry.updated_at)}</div>
            <div style={{ width: 28 }}>
              <button
                onClick={(e) => { e.stopPropagation(); handleDelete(entry.name) }}
                style={{
                  background: 'none', border: 'none', cursor: 'pointer',
                  color: '#4b5563', padding: 4, borderRadius: 3,
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                }}
                onMouseEnter={e => e.currentTarget.style.color = '#ef4444'}
                onMouseLeave={e => e.currentTarget.style.color = '#4b5563'}
              >
                <Trash2 size={13} />
              </button>
            </div>
          </div>
        ))}
      </div>

      {editingEntry && (
        <VaultItemModal
          key={editingEntry.name}
          entry={editingEntry}
          onClose={() => setEditingEntry(null)}
          onSaved={() => { setEditingEntry(null); load() }}
        />
      )}

      {exportResult && (
        <div className="modal-overlay" style={{ position: 'fixed', inset: 0, zIndex: 10001, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.55)' }}>
          <div style={{ background: '#0d1520', border: '1px solid #1e3a4f', borderRadius: 10, padding: 20, width: 420, maxWidth: '90%' }}>
            <div style={{ color: '#e2e8f0', fontSize: 14, fontWeight: 600, marginBottom: 8 }}>Vault Exported</div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#94a3b8', marginBottom: 10 }}>
              Saved to {exportResult.path}. Save this now — it will not be shown again.
            </div>
            {exportResult.skipped > 0 && (
              <div style={{ background: 'rgba(251,191,36,.08)', border: '1px solid rgba(251,191,36,.2)', borderRadius: 5, padding: '7px 10px', marginBottom: 10, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fbbf24' }}>
                Exported {exportResult.exported}, skipped {exportResult.skipped} that could not be decrypted.
              </div>
            )}
            <div style={{ display: 'flex', gap: 8, alignItems: 'center', background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5, padding: '8px 10px', marginBottom: 14 }}>
              <span style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 12, color: '#00b4d8', wordBreak: 'break-all' }}>
                {exportResult.passphrase}
              </span>
              <button
                type="button"
                onClick={() => navigator.clipboard.writeText(exportResult.passphrase)}
                title="Copy"
                style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#4b5563', padding: 4, display: 'flex' }}
              >
                <Copy size={13} />
              </button>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
              <button
                onClick={() => setExportResult(null)}
                style={{ background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 5, padding: '6px 12px', color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
              >
                Done
              </button>
            </div>
          </div>
        </div>
      )}

      {importPath && (
        <div className="modal-overlay" style={{ position: 'fixed', inset: 0, zIndex: 10001, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.55)' }}>
          <form onSubmit={handleImportSubmit} style={{ background: '#0d1520', border: '1px solid #1e3a4f', borderRadius: 10, padding: 20, width: 380, maxWidth: '90%', display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div style={{ color: '#e2e8f0', fontSize: 14, fontWeight: 600 }}>Import Vault</div>
            {importError && (
              <div style={{ background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 5, padding: '7px 10px', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>
                {importError}
              </div>
            )}
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#94a3b8' }}>{importPath}</div>
            <input
              type="password"
              placeholder="Export passphrase"
              value={importPassphrase}
              onChange={e => setImportPassphrase(e.target.value)}
              required
              autoFocus
              style={inputStyle}
            />
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
              <button
                type="button"
                onClick={() => { setImportPath(null); setImportPassphrase('') }}
                style={{ background: 'none', border: '1px solid #1e3a4f', borderRadius: 5, padding: '6px 12px', color: '#94a3b8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
              >
                Cancel
              </button>
              <button
                type="submit"
                style={{ background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 5, padding: '6px 12px', color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
              >
                Import
              </button>
            </div>
          </form>
        </div>
      )}

      {importResult && (
        <div className="modal-overlay" style={{ position: 'fixed', inset: 0, zIndex: 10001, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.55)' }}>
          <div style={{ background: '#0d1520', border: '1px solid #1e3a4f', borderRadius: 10, padding: 20, width: 340, maxWidth: '90%' }}>
            <div style={{ color: '#e2e8f0', fontSize: 14, fontWeight: 600, marginBottom: 10 }}>Import Complete</div>
            {importError && (
              <div style={{ background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 5, padding: '7px 10px', marginBottom: 10, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>
                {importError}
              </div>
            )}
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: '#94a3b8', marginBottom: 14 }}>
              Imported {importResult.imported}, skipped {importResult.skipped} duplicate{importResult.skipped === 1 ? '' : 's'}.
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
              <button
                onClick={() => setImportResult(null)}
                style={{ background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 5, padding: '6px 12px', color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
              >
                Done
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

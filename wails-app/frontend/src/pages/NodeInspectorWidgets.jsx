// Inspector widgets for schema field types that need local state: the
// collection editor (JSON text that only saves when valid) and the
// http.request "Import cURL" box. The rules they apply live in
// nodeInspectorRules.js. The form builder for formFields lives in
// FormFieldsEditor.jsx and is re-exported here with the other widgets.
import { useState, useRef, useEffect } from 'react'
import { api } from '../services/api.js'
import { collectionText, commitCollection, connectionLabel, groupConnectionsByPlatform } from './nodeInspectorRules.js'
export { default as FormFieldsEditor } from './FormFieldsEditor.jsx'

const errorStyle = { fontFamily: 'var(--font-mono)', fontSize: 9, color: '#f87171', marginTop: 4 }
const linkButton = {
  background: 'transparent', border: 'none', cursor: 'pointer',
  fontFamily: 'var(--font-mono)', fontSize: 9, color: '#00b4d8', padding: '4px 0',
}

// CollectionField edits a collection (an object of optional settings) as
// JSON. Text that is not a JSON object stays in the box with an error and
// is not saved, so the saved value is never half-typed.
export function CollectionField({ field, value, onCommit, inputStyle }) {
  const asObject = useRef(value !== null && typeof value === 'object')
  const [text, setText] = useState(() => collectionText(value))
  const [error, setError] = useState('')
  return (
    <>
      <textarea
        className="field-code"
        aria-label={field.label}
        rows={field.rows || 4}
        value={text}
        placeholder={field.placeholder || '{ "key": "value" }'}
        spellCheck={false}
        onChange={e => {
          setText(e.target.value)
          const r = commitCollection(e.target.value, asObject.current)
          setError(r.error)
          if (!r.error) onCommit(r.value)
        }}
        style={{ ...inputStyle, fontFamily: 'var(--font-mono)', fontSize: 11 }}
      />
      {error && <div role="alert" style={errorStyle}>{error}</div>}
    </>
  )
}

// Platform display names (`connect list --all`), read once per window: they
// only change with a new build. A failed read is not kept, so the next
// picker asks again.
let platformNames = null
export function resetPlatformNamesCache() { platformNames = null }

function usePlatformNames() {
  const [names, setNames] = useState(() => platformNames?.value || {})
  useEffect(() => {
    if (platformNames?.value) return
    let alive = true
    if (!platformNames) {
      const entry = { promise: api.platformNames() }
      platformNames = entry
      entry.promise.then(n => {
        if (n && Object.keys(n).length) entry.value = n
        else if (platformNames === entry) platformNames = null
      })
    }
    platformNames.promise.then(n => { if (alive && n) setNames(n) })
    return () => { alive = false }
  }, [])
  return names
}

// AnyPlatformConnections is the picker's option list when the node takes a
// connection of any platform: connections grouped by platform, each group
// labelled with the platform's display name.
export function AnyPlatformConnections({ connections }) {
  const names = usePlatformNames()
  return groupConnectionsByPlatform(connections).map(g => (
    <optgroup key={g.platform} label={names[g.platform] || g.platform}>
      {g.connections.map(c => {
        const id = String(c.id || c.ID || '')
        return <option key={id} value={id}>{connectionLabel(c)}</option>
      })}
    </optgroup>
  ))
}

// CurlImport turns a pasted curl command into the node's config through
// `monoagentcli node curl`; onImport gets the imported config.
export function CurlImport({ onImport, inputStyle }) {
  const [open, setOpen] = useState(false)
  const [command, setCommand] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState(null)
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const run = async () => {
    setBusy(true)
    const res = await api.nodeConfigFromCurl(command)
    if (!alive.current) return
    setBusy(false)
    setResult(res)
    if (!res?.error && res?.config) {
      onImport(res.config)
      setCommand('')
      if (!res.notes?.length) setOpen(false)
    }
  }

  if (!open) {
    return <button type="button" onClick={() => { setOpen(true); setResult(null) }} style={{ ...linkButton, marginBottom: 8 }}>Import cURL…</button>
  }
  return (
    <div style={{ marginBottom: 10 }}>
      <textarea
        aria-label="curl command"
        rows={4}
        value={command}
        onChange={e => setCommand(e.target.value)}
        placeholder={'curl -X POST https://api.example.com/items -H "Content-Type: application/json" -d \'{"name":"x"}\''}
        spellCheck={false}
        style={{ ...inputStyle, fontFamily: 'var(--font-mono)', fontSize: 11 }}
      />
      <div style={{ display: 'flex', gap: 10 }}>
        <button type="button" disabled={busy || !command.trim()} onClick={run} style={linkButton}>
          {busy ? 'Importing…' : 'Import (replaces the request settings)'}
        </button>
        <button type="button" onClick={() => setOpen(false)} style={{ ...linkButton, color: 'var(--text-muted)' }}>Cancel</button>
      </div>
      {result?.error && <div role="alert" style={errorStyle}>{result.error}</div>}
      {result?.notes?.length > 0 && (
        <ul className="field-help" style={{ margin: '4px 0 0', paddingLeft: 14 }}>
          {result.notes.map((n, i) => <li key={i}>{n}</li>)}
        </ul>
      )}
    </div>
  )
}

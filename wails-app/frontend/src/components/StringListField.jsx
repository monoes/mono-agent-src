import { useTranslation } from 'react-i18next'
import '../i18n.js'

// Generic add/remove string-list editor. Extracted from
// orgdesigner/RoleInspector.jsx's original inline Responsibilities editor so
// the same interaction (uncontrolled inputs committing on blur, a "×"
// remove button per row, an "+ Add" button) can be reused for any
// string-array field — currently: responsibilities, and the Org Designer's
// tool-policy arrays (allowTools/denyTools/fileWrite/fileRead/webAllow/
// autoApproveTools).
//
// `resetKey` should be the identity of whatever this list logically belongs
// to (e.g. the selected role's id) — NOT derived from `values` itself. Each
// row is an uncontrolled <input defaultValue>, and React only applies
// `defaultValue` when a row's DOM node is first created; reusing the same
// node across a `values` prop change (which happens whenever the list
// length at that index doesn't change) leaves it showing stale text even
// though `values` itself updated correctly. Folding `resetKey` into each
// row's key forces every row to remount — and reapply its defaultValue —
// whenever the caller's identity changes, while edits within the same
// caller (add/remove/blur-commit) keep the same key and edit in place.
export default function StringListField({ label, values, onChange, placeholder, addLabel, resetKey }) {
  const { t } = useTranslation()
  const items = values || []

  const commit = (next) => onChange(next)

  return (
    <section>
      {label && <div className="form-label">{label}</div>}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {items.map((v, i) => (
          <div key={resetKey != null ? `${resetKey}:${i}` : i} style={{ display: 'flex', gap: 4 }}>
            <input
              className="form-input"
              defaultValue={v}
              onBlur={e => {
                const next = [...items]
                next[i] = e.target.value
                commit(next)
              }}
              style={{ fontSize: 11, padding: '5px 8px', flex: 1 }}
            />
            <button
              className="btn btn-sm btn-ghost"
              onClick={() => commit(items.filter((_, idx) => idx !== i))}
              title={t('ui6Node.remove')}
            >
              &times;
            </button>
          </div>
        ))}
        {items.length === 0 && placeholder && (
          <div style={{ fontSize: 10.5, color: 'var(--text-dim, #6c7b90)', fontStyle: 'italic', marginBottom: 4 }}>
            {placeholder}
          </div>
        )}
        <button
          className="btn btn-sm btn-secondary"
          onClick={() => commit([...items, ''])}
          style={{ alignSelf: 'flex-start' }}
        >
          {addLabel ?? t('ui6Node.addPlus')}
        </button>
      </div>
    </section>
  )
}

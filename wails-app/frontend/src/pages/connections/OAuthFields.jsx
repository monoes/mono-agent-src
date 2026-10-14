// The values an OAuth flow needs before the browser opens, as `connect show
// --json` describes them: an OAuth 1.0a platform's form (consumer key and
// secret, a generic platform's endpoints) or an OAuth 2.0 platform's
// per-tenant connect fields (subdomain, region, environment URL). The CLI
// checks them; this only renders the form and builds `connect oauth`'s
// arguments from it.
import { HelpCircle } from 'lucide-react'
import { api } from '../../services/api.js'

// oauthFormFields are the fields `connect oauth` takes for method, from the
// platform's `connect show --json` view.
export function oauthFormFields(view, method) {
  if (!view) return []
  if (method === 'oauth1') return view.fields?.oauth1 || []
  if (method === 'oauth') return view.oauth?.connect_fields || []
  return []
}

// missingRequired names the required fields with no value yet.
export function missingRequired(fields, values) {
  return fields.filter(f => f.required && !(values[f.key] || '').trim()).map(f => f.key)
}

// oauthFieldArgs splits the filled-in values into the key=value pairs the
// binding passes as --field, and a JSON object of the secret ones for the
// CLI's stdin ('' when there are none), so a secret never reaches argv.
export function oauthFieldArgs(fields, values) {
  const pairs = []
  const secrets = {}
  for (const f of fields) {
    const v = (values[f.key] || '').trim()
    if (!v) continue
    if (f.secret) secrets[f.key] = v
    else pairs.push(`${f.key}=${v}`)
  }
  return { pairs, secretJSON: Object.keys(secrets).length ? JSON.stringify(secrets) : '' }
}

const inputStyle = { width: '100%', fontFamily: 'var(--font-mono)', fontSize: 11, background: 'var(--surface)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '7px 10px', outline: 'none', boxSizing: 'border-box' }
const labelStyle = { display: 'block', fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1.5, marginBottom: 4 }

export default function OAuthFields({ title, fields, values, onChange, disabled }) {
  if (!fields.length) return null
  return (
    <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1.5 }}>{title}</div>
      {fields.map(f => (
        <div key={f.key}>
          <label htmlFor={`oauth-field-${f.key}`} style={labelStyle}>
            {f.label || f.key}{f.required && <span style={{ color: 'var(--red)' }}> *</span>}
          </label>
          <input id={`oauth-field-${f.key}`} type={f.secret ? 'password' : 'text'}
            value={values[f.key] || ''} disabled={disabled}
            onChange={e => onChange({ ...values, [f.key]: e.target.value })}
            autoComplete={f.secret ? 'new-password' : 'off'}
            style={{ ...inputStyle, background: 'var(--elevated)' }} />
          {(f.help_text || f.help_url) && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: 'var(--font-body)', fontSize: 10, color: 'var(--text-muted)', marginTop: 3 }}>
              {f.help_text}
              {f.help_url && (
                <button type="button" onClick={() => api.openURL(f.help_url)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--cyan)', padding: 0, display: 'inline-flex' }} aria-label={`Help for ${f.label || f.key}`}>
                  <HelpCircle size={10} />
                </button>
              )}
            </div>
          )}
        </div>
      ))}
    </div>
  )
}

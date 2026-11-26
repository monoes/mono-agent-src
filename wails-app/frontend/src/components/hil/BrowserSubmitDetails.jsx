import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle } from 'lucide-react'
import { api } from '../../services/api.js'
import { ImageLightbox } from '../chat/ChatAttachments.jsx'
import { quoteClaim } from '../../lib/quoteClaim.js'
import i18nOr from '../../lib/i18nOr.js'

// A browser submit approval: a form an agent tried to submit on a page that
// is not local, held until a person approves it (then `monoagentcli browser
// submit --hil <id>` sends it, once). readonly_data is {origin, url, form:
// {action, method, fields: [{name, type, label, value}], submitter},
// screenshot, requested_by, asciiHost?, unicodeHost?}. Sensitive values
// arrive masked as "•••" and are shown as they are.

export const BROWSER_SUBMIT = 'browser.submit'

const mono = 'var(--font-mono)'
const sectionLabel = { fontSize: 10, fontFamily: mono, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 4 }
export const box = { padding: '7px 10px', background: 'rgba(255,255,255,0.04)', borderRadius: 6, border: '1px solid rgba(255,255,255,0.07)', fontSize: 12, color: 'var(--text-secondary)', wordBreak: 'break-all' }
const cell = { padding: '5px 8px', borderBottom: '1px solid rgba(255,255,255,0.06)', textAlign: 'left', verticalAlign: 'top', wordBreak: 'break-all' }

export function isBrowserSubmit(item) {
  return item?.node_config?.kind === BROWSER_SUBMIT
}

// requesterParts names who asked: the role (a coder chat's own agent is
// "lead"), the org, the agent and the coder chat, whichever are known. An
// agent names itself, so its words are quoted; only MonoAgent's own label
// (by.system: a workflow the user hasn't approved) is not.
export function requesterParts(by) {
  if (!by || typeof by !== 'object') return []
  const q = by.system ? String : (v) => quoteClaim(v)
  const parts = []
  if (by.role) parts.push(by.role === 'lead' && by.conversationId ? i18nOr('ui6SetHil.coder_agent') : i18nOr('ui6SetHil.part_role', { value: q(by.role) }))
  if (by.org) parts.push(i18nOr('ui6SetHil.part_org', { value: q(by.org) }))
  if (by.agentId) parts.push(i18nOr('ui6SetHil.part_agent', { value: q(by.agentId) }))
  if (by.conversationId) parts.push(i18nOr('ui6SetHil.part_chat', { value: q(String(by.conversationId).slice(0, 8)) }))
  return parts
}

// requesterText is the "Requested by" line: MonoAgent's own label as it is,
// anything else as what the agent claims.
export function requesterText(by) {
  const parts = requesterParts(by)
  if (!parts.length) return i18nOr('ui6SetHil.unnamed_agent')
  return by.system ? parts.join(' · ') : i18nOr('ui6SetHil.claims', { parts: parts.join(' · ') })
}

// displayOrigin is the origin as a person reads it. An internationalized
// host (plan C9) comes as the browser's xn-- form in origin, with asciiHost
// and unicodeHost; the Unicode host is shown in its place.
export function displayOrigin(ro) {
  const origin = ro?.origin || ''
  if (ro?.asciiHost && ro?.unicodeHost && origin.includes(ro.asciiHost)) return origin.replace(ro.asciiHost, ro.unicodeHost)
  return origin
}

// AsciiHost is the look-alike check (plan C9): an item whose host has
// non-ASCII characters carries asciiHost, its xn-- form, shown next to the
// Unicode one, since such a host can imitate another site's name. Nothing
// shows for an ASCII host.
export function AsciiHost({ host }) {
  useTranslation() // render again on a language change
  if (!host) return null
  return (
    <div data-testid="ascii-host" style={{ marginTop: 4, fontSize: 11, lineHeight: 1.5 }}>
      {/* No ligatures: "xn--" must not render as a dash. */}
      <div style={{ fontFamily: mono, color: '#e2e8f0', fontVariantLigatures: 'none' }}>
        <span style={{ color: 'var(--text-muted)' }}>{i18nOr('ui6SetHil.ascii_form')} </span>{host}
      </div>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 5, color: '#fbbf24' }}>
        <AlertTriangle size={11} style={{ flexShrink: 0, marginTop: 2 }} />
        <span>{i18nOr('ui6SetHil.check_address')}</span>
      </div>
    </div>
  )
}

export function Section({ label, children }) {
  return (
    <div style={{ marginBottom: 12 }}>
      <div style={sectionLabel}>{label}</div>
      {children}
    </div>
  )
}

// SubmitScreenshot is the page as the agent left it, read through
// ReadHILScreenshot (only from the browser approvals folder).
function SubmitScreenshot({ path }) {
  useTranslation() // render again on a language change
  const [src, setSrc] = useState('')
  const [error, setError] = useState('')
  const [open, setOpen] = useState(false)
  useEffect(() => {
    let live = true
    setSrc('')
    setError('')
    Promise.resolve().then(() => api.readHILScreenshot(path)).then(
      url => { if (live) setSrc(url) },
      err => { if (live) setError(String(err?.message || err)) },
    )
    return () => { live = false }
  }, [path])
  if (error) return <div style={{ ...box, color: 'var(--text-muted)' }}>{i18nOr('ui6SetHil.shot_unavailable', { error })}</div>
  if (!src) return <div style={{ ...box, color: 'var(--text-muted)' }}>{i18nOr('ui6SetHil.shot_loading')}</div>
  return (
    <>
      <button type="button" onClick={() => setOpen(true)} aria-label={i18nOr('ui6SetHil.shot_open')} title={i18nOr('ui6SetHil.shot_large')} style={{
        padding: 0, border: '1px solid rgba(255,255,255,0.07)', borderRadius: 6, background: 'rgba(255,255,255,0.04)',
        cursor: 'zoom-in', display: 'block', overflow: 'hidden', maxWidth: '100%',
      }}>
        <img src={src} alt={i18nOr('ui6SetHil.shot_alt')} style={{ display: 'block', maxWidth: '100%', maxHeight: 260, objectFit: 'contain' }} />
      </button>
      {open && <ImageLightbox src={src} alt={i18nOr('ui6SetHil.shot_alt')} onClose={() => setOpen(false)} />}
    </>
  )
}

// BrowserSubmitDetails replaces the generic "Context Info" of a browser
// submit item; the card's Approve / Reject stay as they are.
export function BrowserSubmitDetails({ item }) {
  useTranslation() // render again on a language change
  const ro = item.readonly_data || {}
  const form = ro.form || {}
  const fields = Array.isArray(form.fields) ? form.fields : []
  const who = requesterText(ro.requested_by)
  return (
    <div data-testid="browser-submit-details" style={{ marginBottom: 20 }}>
      <Section label={i18nOr('ui6SetHil.submits_to')}>
        <div style={box}>
          <div style={{ color: '#e2e8f0', fontWeight: 600 }}>{displayOrigin(ro) || i18nOr('ui6SetHil.unknown_origin')}</div>
          <AsciiHost host={ro.asciiHost} />
          {ro.url && <div style={{ fontFamily: mono, fontSize: 11, marginTop: 2 }}>{ro.url}</div>}
          {(form.method || form.action) && (
            <div style={{ fontFamily: mono, fontSize: 11, marginTop: 4, color: 'var(--text-muted)' }}>
              {(form.method || 'GET').toUpperCase()} {form.action || i18nOr('ui6SetHil.this_page')}
            </div>
          )}
        </div>
      </Section>
      <Section label={i18nOr('ui6SetHil.requested_by')}>
        <div style={box}>{who}</div>
      </Section>
      <Section label={i18nOr('ui6SetHil.form_fields', { n: fields.length })}>
        {fields.length === 0 ? <div style={{ ...box, color: 'var(--text-muted)' }}>{i18nOr('ui6SetHil.no_fields')}</div> : (
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12, ...box, padding: 0 }}>
            <thead>
              <tr style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase' }}>
                <th style={cell}>{i18nOr('ui6SetHil.field')}</th><th style={cell}>{i18nOr('ui6SetHil.type')}</th><th style={cell}>{i18nOr('ui6SetHil.value')}</th>
              </tr>
            </thead>
            <tbody>
              {fields.map((f, i) => (
                <tr key={`${f.name}-${i}`}>
                  <td style={cell}>
                    <div style={{ color: '#e2e8f0' }}>{f.label || f.name}</div>
                    {f.label && f.name && f.label !== f.name && <div style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-muted)' }}>{f.name}</div>}
                  </td>
                  <td style={{ ...cell, fontFamily: mono, fontSize: 11, color: 'var(--text-muted)' }}>{f.type}</td>
                  <td style={{ ...cell, fontFamily: mono, fontSize: 11 }}>{f.value}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
      <Section label={i18nOr('ui6SetHil.submit_control')}>
        <div style={{ ...box, fontFamily: mono, fontSize: 11 }}>{form.submitter || i18nOr('ui6SetHil.by_script')}</div>
      </Section>
      <Section label={i18nOr('ui6SetHil.screenshot')}>
        {ro.screenshot ? <SubmitScreenshot path={ro.screenshot} /> : <div style={{ ...box, color: 'var(--text-muted)' }}>{i18nOr('ui6SetHil.no_shot')}</div>}
      </Section>
    </div>
  )
}

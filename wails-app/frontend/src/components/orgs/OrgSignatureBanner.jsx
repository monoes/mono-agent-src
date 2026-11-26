import { useCallback, useEffect, useRef, useState } from 'react'
import { ShieldAlert } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'

// Signed org definitions (#288, monomind#502). monomind 2.21 runs or
// reloads an org only when the operator signed its definition (roles,
// policies, runtimes, autonomy, automations — not the goal, status or role
// titles). mono-agent re-signs its own edits of a signed org; anything else
// shows this banner. "Review & sign" shows monomind's own review and signs
// only on confirm, and only the definition that was reviewed (its hash,
// instructions files included).

const STATE_TEXT = {
  unsigned: 'This org has no operator signature yet, so monomind will not run it.',
  changed: 'This org changed since it was signed (roles, policies, runtimes, autonomy or automations), so monomind will not run or reload it.',
  'invalid-signature': 'This org’s signature does not verify on this machine, so monomind will not run it.',
  'forbidden-key': 'This org holds a forbidden key, so monomind will not run it. Remove the key, then sign.',
  'invalid-definition': 'This org\u2019s file is unreadable or not a valid org. Fix it (see Validate), then sign.',
}

// States with nothing to sign until the definition itself is fixed.
const NO_SIGN = new Set(['forbidden-key', 'invalid-definition'])

// isSignatureRefusal recognises monomind's (and the CLI's) refusal to start
// or reload an unsigned org in a run's error text.
export function isSignatureRefusal(message = '') {
  return /operator signature|changed since the operator signed|is not signed \(|org sign /.test(String(message))
}

// A failed run elsewhere in the panel asks the banner to look again.
const REFRESH_EVENT = 'org-signature:refresh'
export function requestSignatureRefresh(orgName) {
  window.dispatchEvent(new CustomEvent(REFRESH_EVENT, { detail: { orgName } }))
}
// onSignatureRefresh calls cb when orgName's signature should be read again;
// it returns the unsubscribe.
export function onSignatureRefresh(orgName, cb) {
  const listen = (e) => { if (!e.detail?.orgName || e.detail.orgName === orgName) cb() }
  window.addEventListener(REFRESH_EVENT, listen)
  return () => window.removeEventListener(REFRESH_EVENT, listen)
}

// useOrgSignature loads `org sign --status`, again when refreshKey changes
// (debounced: the designer passes its config stamp) or a refresh is
// requested for this org.
export function useOrgSignature(orgName, refreshKey = '') {
  const [status, setStatus] = useState(null)
  const current = useRef(orgName)
  current.current = orgName
  const refresh = useCallback(async () => {
    const name = orgName
    if (!name) { setStatus(null); return }
    const res = await Promise.resolve().then(() => api.orgSignatureStatus(name)).catch(() => null)
    if (current.current === name) setStatus(res)
  }, [orgName])
  useEffect(() => {
    const id = setTimeout(refresh, refreshKey ? 800 : 0)
    return () => clearTimeout(id)
  }, [refresh, refreshKey])
  useEffect(() => onSignatureRefresh(orgName, refresh), [orgName, refresh])
  return { status, refresh }
}

function ReviewText({ review }) {
  return (
    <div data-testid="org-sign-review">
      <p style={{ margin: '0 0 8px' }}>This is what the org may do. Sign only if you expect all of it.</p>
      <pre style={{
        margin: 0, maxHeight: 320, overflow: 'auto', whiteSpace: 'pre-wrap', fontFamily: 'var(--font-mono)',
        fontSize: 11, background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 4, padding: 8,
      }}>{review}</pre>
    </div>
  )
}

// reviewAndSign shows monomind's review and signs on confirm. Resolves
// true when signed, false when cancelled; rejects with the CLI's refusal.
export async function reviewAndSign(orgName) {
  const review = await api.orgSignatureReview(orgName)
  if (review.blocked_by) throw new Error(`This app was started from an AI-agent shell (${review.blocked_by}); start it normally to sign.`)
  // No hash: the definition changed while monomind reviewed it, so the
  // review may not show what would be signed.
  if (!review.hash) throw new Error(review.message || `org ${orgName} changed during the review; review it again`)
  const ok = await confirm(<ReviewText review={review.review || ''} />, {
    title: `Sign org ${orgName}?`,
    confirmLabel: 'Sign',
    danger: true,
  })
  if (!ok) return false
  await api.orgSign(orgName, review.hash)
  requestSignatureRefresh(orgName) // other views of this org read it again
  return true
}

export default function OrgSignatureBanner({ orgName, refreshKey = '' }) {
  const { status, refresh } = useOrgSignature(orgName, refreshKey)
  const [busy, setBusy] = useState(false)
  const text = status?.supported ? STATE_TEXT[status.state] : null
  if (!text) return null
  // The app inherited an AI agent's marker (started from its shell): the
  // CLI refuses to sign, so say why instead of offering the button.
  const blocked = status.blocked_by
  const canSign = !NO_SIGN.has(status.state) && !blocked
  const onSign = async () => {
    setBusy(true)
    try {
      await reviewAndSign(orgName)
    } catch (err) {
      notify('sign org', err?.message || String(err), err?.code)
    } finally {
      setBusy(false)
      refresh()
    }
  }
  return (
    <div data-testid="org-signature-banner" data-state={status.state} role="alert" style={{
      display: 'flex', alignItems: 'center', gap: 8, padding: '6px 10px', flexShrink: 0,
      background: '#f59e0b14', borderBottom: '1px solid #f59e0b66', color: 'var(--text)', fontSize: 11.5,
    }}>
      <ShieldAlert size={13} style={{ color: '#f59e0b', flexShrink: 0 }} />
      <span style={{ flex: 1 }} title={status.detail || status.message || ''}>
        {text}
        {blocked && (
          <span data-testid="org-sign-blocked" style={{ display: 'block', color: 'var(--text-muted)' }}>
            This app was started from an AI-agent shell ({blocked}); start it normally to sign.
          </span>
        )}
      </span>
      {canSign && (
        <button type="button" onClick={onSign} disabled={busy} style={{
          fontFamily: 'var(--font-mono)', fontSize: 10.5, padding: '3px 8px', borderRadius: 4, cursor: busy ? 'default' : 'pointer',
          background: 'transparent', color: '#f59e0b', border: '1px solid #f59e0b99',
        }}>{busy ? 'Signing…' : 'Review & sign'}</button>
      )}
    </div>
  )
}

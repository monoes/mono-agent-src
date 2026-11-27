import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw } from 'lucide-react'
import * as WailsApp from '../../wailsjs/go/main/App'
import { ApprovalCall } from '../../components/ApprovalCommand.jsx'
import { usePageVisibleRef } from '../../lib/usePageVisible.js'
import { formatDate } from './hilShared.jsx'

// The Human in Loop page's second scope: every pending approval of every
// profile (`hil pending --all`) and every question, approval and gate its
// organizations wait on (`org pending --all`), each card with its profile's
// name. It only reads: an item is decided in its own profile's view.

const POLL_MS = 10000
// `org pending --all` starts monomind for every organization.
const ORG_POLL_MS = 30000
// The kinds `org pending` reports; only the displayed word is translated.
const KIND_KEYS = { question: 'ui7Hil.kind_question', approval: 'ui7Hil.kind_approval', gate: 'ui7Hil.kind_gate' }
const PREVIEW_LIMIT = 600

const preview = v => {
  const s = String((typeof v === 'object' ? JSON.stringify(v) : v) ?? '')
  return s.length > PREVIEW_LIMIT ? `${s.slice(0, PREVIEW_LIMIT)}…` : s
}

const muted = { fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', overflowWrap: 'anywhere' }
const body = { fontSize: 13, color: 'var(--text-secondary)', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', marginTop: 4 }
const cardBox = { background: '#0d1a28', border: '1.5px solid rgba(0,180,216,0.18)', borderRadius: 10, padding: '12px 16px', marginBottom: 12 }
const cardHead = { display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }
const cardTitle = { fontSize: 13, fontWeight: 600, color: '#e2e8f0', overflowWrap: 'anywhere' }

function ProfileChip({ item }) {
  const { t } = useTranslation()
  const profile = item.profile_name || item.profile_id
  return (
    <span data-testid="hil-all-profile" title={t('hilAll.profile', { name: profile })} style={{ padding: '2px 8px', borderRadius: 10, background: 'rgba(139,92,246,0.15)', border: '1px solid rgba(139,92,246,0.4)', color: '#a78bfa', fontSize: 11, fontWeight: 600 }}>
      {profile}
    </span>
  )
}

function PendingCard({ item }) {
  const { t } = useTranslation()
  const send = item.send
  const fields = send ? [] : Object.entries({ ...item.readonly_data, ...item.editable_data })
  return (
    <div data-testid="hil-all-card" style={cardBox}>
      <div style={cardHead}>
        <ProfileChip item={item} />
        <span style={cardTitle}>
          {send ? t('hilAll.send', { action: send.action, channel: send.channel, recipient: send.recipient?.name ?? '', interpolation: { escapeValue: false } }) : item.node_name}
        </span>
      </div>
      <div style={{ ...muted, marginTop: 4 }}>
        {item.workflow_name ? `${item.workflow_name} · ` : ''}{t('hilAll.since', { when: formatDate(item.created_at), interpolation: { escapeValue: false } })}
      </div>
      {send?.account?.handle && <div style={muted}>{t('hilAll.from', { account: send.account.handle, interpolation: { escapeValue: false } })}</div>}
      {send?.host && <div style={muted}>{t('hilAll.via', { host: send.host, interpolation: { escapeValue: false } })}</div>}
      {send?.details?.map(d => <div key={d.key} style={muted}>{d.key}: {d.value}</div>)}
      {send?.subject && <div style={body}>{t('hilAll.subject', { subject: send.subject, interpolation: { escapeValue: false } })}</div>}
      {send?.text && <div style={{ ...body, maxHeight: 200, overflowY: 'auto' }}>{send.text}</div>}
      {send?.attachments?.map((a, i) => <div key={i} style={muted}>{t('hilAll.attachment', { name: a.path || a.url || a.name, interpolation: { escapeValue: false } })}</div>)}
      {send?.unreviewable && <div style={{ ...body, color: '#f59e0b' }}>{t('hilAll.unreviewable', { reason: send.unreviewable, interpolation: { escapeValue: false } })}</div>}
      {fields.map(([k, v]) => <div key={k} style={body}><span style={muted}>{k}: </span>{preview(v)}</div>)}
    </div>
  )
}

// An organization's question, approval or gate. No buttons: it is answered or
// decided in its profile's view.
function OrgPendingCard({ item }) {
  const { t } = useTranslation()
  const kind = Object.hasOwn(KIND_KEYS, item.kind) ? t(KIND_KEYS[item.kind]) : item.kind
  const title = item.action || item.name || t('ui7Hil.kind_for_role', { kind, role: item.role })
  const text = item.text || item.name
  return (
    <div data-testid="hil-all-org-card" style={{ ...cardBox, border: '1.5px solid rgba(139,92,246,0.25)' }}>
      <div style={cardHead}>
        <ProfileChip item={item} />
        <span style={muted}>{t('ui8HilOrg.org', { name: item.org })}</span>
        <span style={{ ...cardTitle, flex: 1 }}>{title}</span>
        <span style={muted}>{kind}</span>
      </div>
      <div style={{ ...muted, marginTop: 4 }}>
        {t('ui7Hil.role', { role: item.role || t('ui7Hil.operator') })}
        {item.ts > 0 && ` · ${t('hilAll.since', { when: new Date(item.ts).toLocaleString() })}`}
      </div>
      {text && <div style={{ ...body, maxHeight: 200, overflowY: 'auto' }}>{text}</div>}
      {item.kind === 'approval' && <ApprovalCall command={item.command} truncated={item.command_truncated} cwd={item.cwd} input={item.input} />}
    </div>
  )
}

// usePolled holds what read() last gave: read when shown, every `every` ms
// while the page is visible, and on load(). One read runs at a time, and a
// failed one keeps what was there.
function usePolled(read, every) {
  const [data, setData] = useState(null)
  const [error, setError] = useState('')
  const visible = usePageVisibleRef()
  const inFlight = useRef(false)

  const load = useCallback(async () => {
    if (inFlight.current) return
    inFlight.current = true
    try {
      setData(await read())
      setError('')
    } catch (e) {
      setError(e?.message ?? String(e))
    } finally {
      inFlight.current = false
    }
  }, [read])

  useEffect(() => {
    load()
    const timer = setInterval(() => { if (visible.current) load() }, every)
    return () => clearInterval(timer)
  }, [load, visible, every])

  return { data, error, load }
}

function AllProfilesPending() {
  const { t } = useTranslation()
  const readItems = useCallback(async () => {
    const res = JSON.parse(await WailsApp.HILPendingAll())
    if (!Array.isArray(res)) throw new Error(res?.error || t('hilAll.badAnswer'))
    return res
  }, [t])
  const readOrgs = useCallback(async () => {
    const res = JSON.parse(await WailsApp.HILOrgPendingAll())
    if (!Array.isArray(res?.items)) throw new Error(res?.error || t('hilAll.badAnswer'))
    return { items: res.items, errors: res.errors || [] }
  }, [t])
  const { data: items, error, load } = usePolled(readItems, POLL_MS)
  const { data: orgs, error: orgError, load: loadOrgs } = usePolled(readOrgs, ORG_POLL_MS)
  const refresh = () => { load(); loadOrgs() }
  const nothing = items?.length === 0 && !orgError && orgs?.items.length === 0 && orgs.errors.length === 0
  const alert = { ...body, color: '#ef4444', marginBottom: 12 }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12 }}>
        <span style={{ ...muted, flex: 1 }}>{t('hilAll.readOnly')}</span>
        <button type="button" onClick={refresh} title={t('hilAll.refresh')} aria-label={t('hilAll.refresh')} style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', display: 'flex' }}>
          <RefreshCw size={13} />
        </button>
      </div>
      {error && <div role="alert" style={alert}>{t('hilAll.error', { error, interpolation: { escapeValue: false } })}</div>}
      {items === null && !error && <div style={muted}>{t('hilAll.loading')}</div>}
      {nothing && <div style={muted}>{t('hilAll.empty')}</div>}
      {items?.map(item => <PendingCard key={`${item.profile_id}/${item.id}`} item={item} />)}

      {orgs === null && !orgError && <div style={muted}>{t('ui8HilOrg.loading')}</div>}
      {(orgError || orgs?.items.length > 0 || orgs?.errors.length > 0) && <h3 style={{ ...muted, textTransform: 'uppercase', margin: '16px 0 8px' }}>{t('ui8HilOrg.heading')}</h3>}
      {orgError && <div role="alert" style={alert}>{t('ui8HilOrg.error', { error: orgError })}</div>}
      {orgs?.errors.length > 0 && (
        <div role="alert" style={alert}>
          <div>{t('ui8HilOrg.unread')}</div>
          {orgs.errors.map((e, i) => (
            <div key={i}>{t(e.org ? 'ui8HilOrg.orgError' : 'ui8HilOrg.profileError', { profile: e.profile_name || e.profile_id, org: e.org, error: e.error })}</div>
          ))}
        </div>
      )}
      {orgs?.items.map(item => <OrgPendingCard key={`${item.profile_id}/${item.org}/${item.kind}/${item.ref}`} item={item} />)}
    </div>
  )
}

// HilScope switches the page between its own profile's queue (children) and
// every profile's.
export function HilScope({ children }) {
  const { t } = useTranslation()
  const [all, setAll] = useState(false)
  const pill = on => ({
    padding: '4px 10px', borderRadius: 20, fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600, cursor: 'pointer',
    background: on ? 'rgba(139,92,246,0.2)' : 'rgba(255,255,255,0.04)',
    border: on ? '1px solid rgba(139,92,246,0.4)' : '1px solid rgba(255,255,255,0.08)',
    color: on ? '#a78bfa' : 'var(--text-muted)',
  })
  return (
    <>
      <div role="group" style={{ display: 'flex', gap: 6, marginBottom: 12 }}>
        <button type="button" aria-pressed={!all} onClick={() => setAll(false)} style={pill(!all)}>{t('hilAll.thisProfile')}</button>
        <button type="button" aria-pressed={all} onClick={() => setAll(true)} style={pill(all)}>{t('hilAll.allProfiles')}</button>
      </div>
      {all ? <AllProfilesPending /> : children}
    </>
  )
}

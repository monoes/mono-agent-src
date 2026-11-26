// Needs you (U16, C-41): pending decisions routed to a human, with how long
// each has waited and — when the CLI reports it — how long until the org
// idle-stops. Items autonomy is still handling are listed below and can be
// decided by hand at any time. Replaces the old auto-approve toggle (C-42):
// nothing here resolves anything without a click.
import { useCallback, useEffect, useRef, useState } from 'react'
import { CheckCircle2, XCircle, MessageCircleQuestion, ShieldAlert, KeyRound, Clock, Hourglass, Workflow } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { KVBlock } from '../KVBlock.jsx'
import { ApprovalCall } from '../ApprovalCommand.jsx'
import { NoRequestID } from '../NoRequestID.jsx'
import { pendingRawItems, joinNeedsYou } from './needsYouModel.js'
import { waitedLabel, idleStopRemainingMs, idleStopLabel } from './waiting.js'
import { Card, Chip, TierChip, mono, mutedText, sectionLabel } from './ui.jsx'

const POLL_MS = 6000

const KIND_ICONS = { question: MessageCircleQuestion, ask_human: MessageCircleQuestion, gate: ShieldAlert, approval: KeyRound, hil: Workflow }

function QuestionAnswer({ onSubmit, busy }) {
  const [text, setText] = useState('')
  return (
    <div style={{ display: 'flex', gap: 6 }}>
      <input
        type="text"
        aria-label="Answer"
        value={text}
        onChange={e => setText(e.target.value)}
        placeholder="Type an answer…"
        style={{ flex: 1, ...mono, fontSize: 11, background: 'var(--elevated)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '6px 8px', outline: 'none' }}
      />
      <button className="btn btn-primary btn-sm" disabled={busy || !text.trim()} onClick={() => { onSubmit(text.trim()); setText('') }}>
        Answer
      </button>
    </div>
  )
}

function WaitingLine({ since, idleSeconds, fetchedAt, now }) {
  const waited = waitedLabel(since, now)
  const remaining = idleStopRemainingMs(idleSeconds, fetchedAt, now)
  if (!waited && remaining == null) return null
  const urgent = remaining != null && remaining < 5 * 60_000
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
      {waited && <span style={{ ...mutedText, display: 'inline-flex', alignItems: 'center', gap: 3 }}><Clock size={10} />{waited}</span>}
      {remaining != null && (
        <Chip color={urgent ? 'var(--red, #ef4444)' : '#eab308'} title="The org stops when it stays idle this long">
          <Hourglass size={9} />{idleStopLabel(remaining)}
        </Chip>
      )}
    </div>
  )
}

function PendingCard({ orgName, entry, fetchedAt, now, busyKey, onAct }) {
  const { item, raw } = entry
  const kind = item?.kind || raw?.kind
  const Icon = KIND_ICONS[kind] || KeyRound
  const key = raw?.key || `${item?.kind}:${item?.ref}`
  const busy = busyKey === key
  const summary = item?.summary || raw?.text || ''
  return (
    <Card>
      <div data-testid="pending-item" style={{ display: 'flex', flexDirection: 'column', gap: 7 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          <Icon size={11} style={{ color: 'var(--text-muted)' }} />
          <span style={sectionLabel}>{kind === 'ask_human' ? 'question' : kind}</span>
          {item?.tier && <TierChip tier={item.tier} />}
          {item?.class && <span style={{ ...mono, fontSize: 10.5, color: 'var(--text-secondary)' }}>{item.class}</span>}
          {(item?.requester || raw?.role) && <span style={mutedText}>from {item?.requester || raw?.role}</span>}
          {raw?.count > 1 && <Chip>{raw.count} calls</Chip>}
        </div>
        {summary && <div style={{ ...mono, fontSize: 11.5, color: 'var(--text)', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{summary}</div>}
        {raw?.calls?.map((c, i) => <ApprovalCall key={i} {...c} />)}
        <WaitingLine since={item?.waiting_since ?? raw?.since} idleSeconds={item?.idle_stop_in_seconds} fetchedAt={fetchedAt} now={now} />
        {raw && (
          <details>
            <summary style={{ cursor: 'pointer', ...mutedText }}>Details</summary>
            <KVBlock obj={raw.raw} />
          </details>
        )}
        {raw?.kind === 'question' && (
          <QuestionAnswer busy={busy} onSubmit={(answer) => onAct(key, () => api.answerOrgQuestion(orgName, raw.ref, answer))} />
        )}
        {raw?.kind === 'gate' && (
          <div style={{ display: 'flex', gap: 6 }}>
            <button className="btn btn-primary btn-sm" disabled={busy} onClick={() => onAct(key, () => api.gateApproveOrgAction(orgName, raw.ref, ''))}>
              <CheckCircle2 size={11} /> Approve
            </button>
            <button className="btn btn-danger btn-sm" disabled={busy} onClick={() => onAct(key, () => api.gateRejectOrgAction(orgName, raw.ref, ''))}>
              <XCircle size={11} /> Reject
            </button>
          </div>
        )}
        {raw?.kind === 'approval' && !raw.requestId && <NoRequestID org={orgName} role={raw.role} action={raw.action} />}
        {raw?.kind === 'approval' && raw.requestId && (
          <div style={{ display: 'flex', gap: 6 }}>
            <button className="btn btn-primary btn-sm" disabled={busy} onClick={() => onAct(key, () => api.approveOrgAction(orgName, raw.role, raw.action, raw.requestId))}>
              <CheckCircle2 size={11} /> Approve
            </button>
            <button className="btn btn-danger btn-sm" disabled={busy} onClick={() => onAct(key, () => api.denyOrgAction(orgName, raw.role, raw.action, raw.requestId))}>
              <XCircle size={11} /> Deny
            </button>
          </div>
        )}
        {!raw && kind === 'hil' && <div style={mutedText}>Resolve this in Human in Loop.</div>}
      </div>
    </Card>
  )
}

export default function NeedsYouPanel({ orgName, onCountChange }) {
  const [state, setState] = useState(null) // { available, mine, others, fetchedAt }
  const [loading, setLoading] = useState(true)
  const [busyKey, setBusyKey] = useState(null)
  const [now, setNow] = useState(() => Date.now())
  const onCountRef = useRef(onCountChange)
  onCountRef.current = onCountChange

  const load = useCallback(async () => {
    if (!orgName) return
    const [needsYou, questions, gates, approvals] = await Promise.all([
      api.listNeedsYou(orgName),
      api.getOrgQuestions(orgName),
      api.getOrgGates(orgName),
      api.getOrgApprovals(orgName),
    ])
    const joined = joinNeedsYou(needsYou, pendingRawItems(questions, gates, approvals))
    setState({ ...joined, fetchedAt: Date.now() })
    setLoading(false)
    onCountRef.current?.(joined.mine.length)
  }, [orgName])

  useEffect(() => {
    setLoading(true)
    setState(null)
    load()
    const iv = setInterval(load, POLL_MS)
    const tick = setInterval(() => setNow(Date.now()), 1000)
    return () => { clearInterval(iv); clearInterval(tick) }
  }, [load])

  const act = useCallback(async (key, fn) => {
    setBusyKey(key)
    try {
      const res = await fn()
      if (res?.error) notify('org decision', res.error)
    } catch (e) {
      notify('org decision', e?.message || String(e))
    } finally {
      setBusyKey(null)
      load()
    }
  }, [load])

  if (loading && !state) return <div style={{ display: 'flex', justifyContent: 'center', padding: 8 }}><div className="spinner" /></div>
  if (!state) return null

  const card = (entry) => (
    <PendingCard key={entry.raw?.key || `${entry.item?.kind}:${entry.item?.ref}`} orgName={orgName} entry={entry}
      fetchedAt={state.fetchedAt} now={now} busyKey={busyKey} onAct={act} />
  )

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      {!state.available && (
        <div style={mutedText}>Autonomy routing is unavailable, so every pending item is listed.</div>
      )}
      {state.mine.length === 0
        ? <div style={{ ...mono, fontSize: 11, color: 'var(--text-muted)' }}>Nothing needs you.</div>
        : state.mine.map(card)}
      {state.others.length > 0 && (
        <>
          <div style={{ ...sectionLabel, marginTop: 6 }}>Being decided by autonomy ({state.others.length})</div>
          {state.others.map(raw => card({ item: null, raw }))}
        </>
      )}
    </div>
  )
}

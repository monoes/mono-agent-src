// Decisions feed (U16/U17): every routed decision from `org autonomy
// decisions` — who resolved it, its tier, the verdict, the decider's
// rationale, cost, and latency. Newest first. When the jev decider was asked,
// the CLI adds a `jev` summary ({decided, verdict, p, threshold}) next to the
// raw `probabilities`; the row shows "Jev p=0.93" (or that the model decided
// below the threshold) and the per-verdict distribution on expand / hover.
import { useCallback, useEffect, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { api } from '../../services/api.js'
import { formatDuration, toMillis } from './waiting.js'
import { Card, Chip, TierChip, mono, mutedText, smallBtn } from './ui.jsx'
import { useTranslation } from 'react-i18next'
import i18n from '../../i18n.js'

export const VERDICTS = ['approved', 'denied', 'answered', 'escalated', 'failed']

export const VERDICT_COLORS = {
  approved: 'var(--green-neon, #22c55e)',
  denied: 'var(--red, #ef4444)',
  answered: 'var(--cyan, #00b4d8)',
  escalated: '#eab308',
  failed: 'var(--red, #ef4444)',
}

function resolverLabel(resolver) {
  if (!resolver) return i18n.t('ui6Orgs.resolver_unknown')
  if (resolver === 'rule') return i18n.t('ui6Orgs.resolver_rule')
  if (resolver === 'human') return i18n.t('ui6Orgs.resolver_you')
  return resolver
}

function formatCost(usd) {
  if (usd == null) return null
  const n = Number(usd)
  if (!n) return '$0'
  if (n < 0.0001) return '<$0.0001'
  // Decider calls usually cost cents or less; keep them readable.
  return n < 1 ? `$${n.toFixed(4)}` : `$${n.toFixed(2)}`
}

const fmtP = (p) => (Number(p) || 0).toFixed(2)
const fmtThreshold = (t) => String(Math.round(Number(t) * 100) / 100)
const verdictName = (v) => i18n.t(`ui6Orgs.verdict_${v}`, { defaultValue: v })

/** Verdicts with their probabilities, highest first. */
export function jevDistribution(probabilities) {
  return Object.entries(probabilities || {})
    .map(([verdict, p]) => ({ verdict, p: Number(p) || 0 }))
    .sort((a, b) => b.p - a.p || a.verdict.localeCompare(b.verdict))
}

/**
 * One-line summary of a decision's Jev answer, or null when Jev was not asked.
 * A row answered by a decision provider other than TypeSafe carries
 * `jev.provider` and reads "Jev · <provider> p=…"; TypeSafe rows are unchanged.
 */
export function jevSummary(d) {
  const j = d?.jev
  if (!j) return null
  const who = j.provider ? `Jev · ${j.provider}` : 'Jev'
  if (j.decided) return `${who} p=${fmtP(j.p)}`
  if (j.threshold == null) return i18n.t('ui6Orgs.jev_model_decided', { who, p: fmtP(j.p) })
  return i18n.t('ui6Orgs.jev_model_decided_below', { who, p: fmtP(j.p), threshold: fmtThreshold(j.threshold) })
}

const jevColor = (d) => (d.jev?.decided ? 'var(--cyan, #00b4d8)' : '#eab308')

function JevChip({ d, open, onToggle }) {
  const summary = jevSummary(d)
  if (!summary) return null
  const dist = jevDistribution(d.probabilities)
  const title = dist.map(x => `${verdictName(x.verdict)} ${fmtP(x.p)}`).join(', ')
  return (
    <button
      data-testid="jev-chip"
      aria-expanded={open}
      title={title ? `Jev: ${title}` : undefined}
      onClick={onToggle}
      disabled={dist.length === 0}
      style={{ display: 'inline-flex', padding: 0, background: 'none', border: 'none', cursor: dist.length ? 'pointer' : 'default' }}
    >
      <Chip color={jevColor(d)} style={d.jev.decided ? undefined : { borderStyle: 'dashed' }}>{summary}</Chip>
    </button>
  )
}

function JevDistribution({ d }) {
  const dist = jevDistribution(d.probabilities)
  if (dist.length === 0) return null
  const color = jevColor(d)
  return (
    <div data-testid="jev-distribution" style={{ display: 'grid', gridTemplateColumns: 'max-content minmax(80px, 240px) max-content', alignItems: 'center', gap: '3px 8px' }}>
      {dist.map(x => (
        <div key={x.verdict} style={{ display: 'contents' }}>
          <span style={{ ...mono, fontSize: 10, color: x.verdict === d.jev.verdict ? 'var(--text)' : 'var(--text-muted)' }}>{verdictName(x.verdict)}</span>
          <div style={{ height: 4, borderRadius: 2, background: 'var(--border)', overflow: 'hidden' }}>
            <div style={{ width: `${Math.round(x.p * 100)}%`, height: '100%', background: color }} />
          </div>
          <span style={{ ...mono, fontSize: 10, color: 'var(--text-muted)' }}>{fmtP(x.p)}</span>
        </div>
      ))}
    </div>
  )
}

function DecisionRow({ d }) {
  const { t } = useTranslation()
  const [jevOpen, setJevOpen] = useState(false)
  const color = VERDICT_COLORS[d.verdict] || 'var(--text-muted)'
  const at = toMillis(d.created_at)
  const cost = formatCost(d.cost_usd)
  return (
    <Card style={{ padding: '8px 12px' }}>
      <div data-testid="decision-row" style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          <Chip color={color} style={d.verdict === 'failed' ? { borderStyle: 'dashed' } : undefined}>{verdictName(d.verdict)}</Chip>
          <TierChip tier={d.tier} />
          <span style={{ ...mono, fontSize: 11, color: 'var(--text)' }}>{d.class}</span>
          {d.requester && <span style={mutedText}>{t('ui6Orgs.from_who', { who: d.requester })}</span>}
          <span style={{ flex: 1 }} />
          <span style={mutedText} title={d.created_at}>{at ? new Date(at).toLocaleTimeString() : ''}</span>
        </div>
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', ...mutedText }}>
          {/* A provider's resolver (jev:<provider>/<model>) can be long and has
              no break point: it truncates, the full label in the tooltip. */}
          <span data-testid="decision-resolver" title={resolverLabel(d.resolver)} style={{ display: 'inline-flex', gap: 4, minWidth: 0, maxWidth: '100%' }}>
            {t('ui6Orgs.by_prefix')} <b style={{ color: 'var(--text-secondary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: 280 }}>{resolverLabel(d.resolver)}</b>
          </span>
          {d.level && <span>{t('ui6Orgs.at_level', { level: t(`ui6Orgs.level_${d.level}`, { defaultValue: d.level }) })}</span>}
          {cost && <span>{cost}</span>}
          {d.latency_ms != null && <span>{formatDuration(d.latency_ms) || `${d.latency_ms} ms`}</span>}
          {d.chain_id && <span title={t('ui6Orgs.chain')}>{d.chain_id}</span>}
          <JevChip d={d} open={jevOpen} onToggle={() => setJevOpen(v => !v)} />
        </div>
        {jevOpen && d.jev && <JevDistribution d={d} />}
        {d.rationale && <div style={{ ...mono, fontSize: 11, color: 'var(--text-secondary)', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{d.rationale}</div>}
        {d.answer_text && <div style={{ ...mono, fontSize: 11, color: 'var(--text)', whiteSpace: 'pre-wrap' }}>{t('ui6Orgs.answer_is', { answer: d.answer_text })}</div>}
      </div>
    </Card>
  )
}

export default function DecisionsFeed({ orgName, run = '' }) {
  const { t } = useTranslation()
  const [decisions, setDecisions] = useState(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [verdict, setVerdict] = useState('')

  const load = useCallback(async () => {
    if (!orgName) return
    const res = await api.listOrgDecisionLog(orgName, run)
    setLoading(false)
    if (!res || res.error) {
      setError(res?.error || i18n.t('ui6Orgs.decisions_load_failed'))
      return
    }
    setError('')
    const list = Array.isArray(res.decisions) ? res.decisions : []
    setDecisions([...list].sort((a, b) => (toMillis(b.created_at) || 0) - (toMillis(a.created_at) || 0)))
  }, [orgName, run])

  useEffect(() => {
    setLoading(true)
    setDecisions(null)
    load()
    const iv = setInterval(load, 15_000)
    return () => clearInterval(iv)
  }, [load])

  const shown = (decisions || []).filter(d => !verdict || d.verdict === verdict)
  const totalCost = (decisions || []).reduce((s, d) => s + (Number(d.cost_usd) || 0), 0)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <select aria-label={t('ui6Orgs.filter_verdict')} className="filter-select" value={verdict} onChange={e => setVerdict(e.target.value)} style={{ maxWidth: 160 }}>
          <option value="">{t('ui6Orgs.all_verdicts')}</option>
          {VERDICTS.map(v => <option key={v} value={v}>{verdictName(v)}</option>)}
        </select>
        {decisions && decisions.length > 0 && (
          <span style={mutedText}>{t('ui6Orgs.decision_count', { count: decisions.length, cost: formatCost(totalCost) })}</span>
        )}
        <span style={{ flex: 1 }} />
        <button style={smallBtn} onClick={() => { setLoading(true); load() }} aria-label={t('ui6Orgs.refresh_decisions')}><RefreshCw size={10} /></button>
      </div>
      {loading && !decisions && <div style={{ display: 'flex', justifyContent: 'center', padding: 8 }}><div className="spinner" /></div>}
      {error && <div style={{ ...mono, fontSize: 11, color: '#f87171' }}>{error}</div>}
      {!loading && !error && decisions && shown.length === 0 && (
        <div style={{ ...mono, fontSize: 11, color: 'var(--text-muted)' }}>
          {verdict ? t('ui6Orgs.no_decisions_verdict', { verdict: verdictName(verdict) }) : t('ui6Orgs.no_decisions')}
        </div>
      )}
      {shown.map(d => <DecisionRow key={d.id} d={d} />)}
    </div>
  )
}

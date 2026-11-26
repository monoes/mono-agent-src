// Scheduled ticks of an org that did not start a run (#342), from monomind's
// schedule-audit.jsonl via `org schedule-audit`: a refused start (host
// preflight, eval gate, daemon lock), a tick that yielded to a live run, and
// ticks held for one catch-up run. Renders nothing when there are none. An
// event this build does not know is shown under its own name, never dropped.
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, onOrgEvent } from '../../services/api.js'
import { Chip, mutedText, sectionLabel } from '../orgs/ui.jsx'
import { useTranslation } from 'react-i18next'
import i18n from '../../i18n.js'

const KINDS = {
  refused: { labelKey: 'ui6OrgsCanvas.tick_refused', color: 'var(--red, #ef4444)', hintKey: 'ui6OrgsCanvas.tick_refused_hint' },
  skipped: { labelKey: 'ui6OrgsCanvas.tick_skipped', color: '#eab308', hintKey: 'ui6OrgsCanvas.tick_skipped_hint' },
  coalesced: { labelKey: 'ui6OrgsCanvas.tick_coalesced', color: 'var(--text-muted)', hintKey: 'ui6OrgsCanvas.tick_coalesced_hint' },
}

export function kindMeta(kind, event) {
  const known = KINDS[kind]
  if (known) return { label: i18n.t(known.labelKey), color: known.color, hint: i18n.t(known.hintKey) }
  return { label: event || i18n.t('ui6OrgsCanvas.tick_other'), color: 'var(--text-muted)', hint: i18n.t('ui6OrgsCanvas.tick_unknown_hint') }
}

export function normalizeAudit(res) {
  if (!res || res.error || !Array.isArray(res.entries)) return null
  return res.entries.filter(e => e && typeof e === 'object')
}

export default function ScheduleAuditPanel({ orgName, live = false }) {
  const { t } = useTranslation()
  const [entries, setEntries] = useState([])
  const request = useRef(0)
  const load = useCallback(async () => {
    if (!orgName) return
    const mine = ++request.current
    const list = normalizeAudit(await api.getOrgScheduleAudit(orgName))
    if (mine === request.current && list) setEntries(list)
  }, [orgName])

  useEffect(() => {
    setEntries([])
    load()
    // A refused start writes the audit file without creating a run bus.
    // Keep the visible org audit current even when no run is selected.
    const timer = setInterval(load, 5000)
    return () => { clearInterval(timer); request.current++ }
  }, [load])
  useEffect(() => {
    if (!live || !orgName) return undefined
    return onOrgEvent((p) => {
      if (p?.orgName === orgName && p.event?.type === 'audit' && String(p.event.reason || '').startsWith('scheduled-')) load()
    })
  }, [live, orgName, load])

  if (entries.length === 0) return null
  return (
    <section aria-label={t('ui6OrgsCanvas.scheduled_ticks')} style={{ display: 'flex', flexDirection: 'column', gap: 4, marginBottom: 8 }}>
      <span style={sectionLabel}>{t('ui6OrgsCanvas.scheduled_ticks_heading')}</span>
      <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 4 }}>
        {entries.map((e, i) => {
          const m = kindMeta(e.kind, e.event)
          return (
            <li key={`${e.ts}-${i}`} style={{ ...mutedText, display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'baseline', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '4px 8px' }}>
              <Chip color={m.color} title={m.hint}>{m.label}</Chip>
              {e.at && <span>{new Date(e.at).toLocaleString()}</span>}
              <span style={{ color: 'var(--text-secondary)', wordBreak: 'break-word' }}>{e.msg}</span>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

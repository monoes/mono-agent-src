// Automations drawer (plan §7.4): the org's automations with their alias,
// an outbound badge (real-world side effects), open-in-editor, and remove;
// plus the Unassigned library to add from. Rows are drag sources: drop one
// on empty canvas for an automation role, or on an agent role to grant it.
import { useCallback, useState } from 'react'
import { Workflow, GripVertical, ExternalLink, Trash2, Plus, RefreshCw, AlertTriangle, Send } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'
import { Chip, mono, mutedText, sectionLabel, smallBtn } from '../orgs/ui.jsx'
import { useTranslation } from 'react-i18next'
import i18n from '../../i18n.js'

/** Role-id-shaped alias from a workflow name (plan §2 naming rule). */
export function slugifyAlias(name) {
  const s = String(name || '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .slice(0, 40)
  return /^[a-z]/.test(s) ? s : `automation_${s || 'new'}`.replace(/_+$/, '').slice(0, 40)
}

function LibraryRow({ wf, onAdd, busy }) {
  const { t } = useTranslation()
  const [alias, setAlias] = useState(() => slugifyAlias(wf.name))
  const valid = /^[a-z][a-z0-9_]{0,39}$/.test(alias) && !['human', 'workflow', 'status', 'output'].includes(alias)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, padding: '6px 8px', border: '1px solid var(--border)', borderRadius: 'var(--radius)' }}>
      <span style={{ ...mono, fontSize: 11, color: 'var(--text)' }}>{wf.name || wf.id}</span>
      <div style={{ display: 'flex', gap: 4 }}>
        <input
          aria-label={t('ui6OrgsMore.alias_for', { name: wf.name || wf.id })}
          value={alias}
          onChange={e => setAlias(e.target.value)}
          style={{ flex: 1, minWidth: 0, ...mono, fontSize: 10.5, padding: '3px 6px', background: 'var(--bg)', border: `1px solid ${valid ? 'var(--border)' : 'var(--red, #ef4444)'}`, borderRadius: 4, color: 'var(--text)' }}
        />
        <button style={smallBtn} disabled={busy || !valid} onClick={() => onAdd(wf, alias)}>
          <Plus size={10} /> {t('ui6OrgsMore.add')}
        </button>
      </div>
      {!valid && <span style={{ ...mono, fontSize: 9.5, color: '#f87171' }}>{t('ui6OrgsMore.alias_rule')}</span>}
    </div>
  )
}

export default function AutomationsDrawer({ orgName, automations, grants, loading, error, onRefresh, onDragStart, onOpenWorkflow, onCreateWorkflow }) {
  const { t } = useTranslation()
  const [libraryOpen, setLibraryOpen] = useState(false)
  const [library, setLibrary] = useState(null)
  const [libraryError, setLibraryError] = useState('')
  const [busy, setBusy] = useState(false)

  const loadLibrary = useCallback(async () => {
    setLibraryError('')
    const res = await api.listUnassignedAutomations()
    if (!res || res.error) {
      setLibrary([])
      setLibraryError(res?.error || i18n.t('ui6OrgsMore.library_load_failed'))
      return
    }
    setLibrary(Array.isArray(res.workflows) ? res.workflows : [])
  }, [])

  const toggleLibrary = () => {
    const next = !libraryOpen
    setLibraryOpen(next)
    if (next) loadLibrary()
  }

  const add = async (wf, alias) => {
    setBusy(true)
    try {
      const res = await api.addOrgAutomation(orgName, wf.id, alias)
      if (!res || res.error) { notify(i18n.t('ui6OrgsMore.op_add_automation'), res?.error || i18n.t('ui6OrgsMore.add_automation_failed')); return }
      await Promise.all([onRefresh?.(), loadLibrary()])
    } finally {
      setBusy(false)
    }
  }

  const remove = async (a) => {
    const used = (grants || []).filter(g => g.alias === a.alias).length
    const ok = await confirm(
      used ? i18n.t('ui6OrgsMore.remove_with_grants', { alias: a.alias, count: used }) : i18n.t('ui6OrgsMore.remove_from_org_confirm', { alias: a.alias, org: orgName }),
      { title: i18n.t('ui6OrgsMore.remove_automation'), confirmLabel: i18n.t('ui6OrgsMore.remove'), danger: true },
    )
    if (!ok) return
    setBusy(true)
    try {
      const res = await api.removeOrgAutomation(orgName, a.alias)
      if (!res || res.error) { notify(i18n.t('ui6OrgsMore.op_remove_automation'), res?.error || i18n.t('ui6OrgsMore.remove_automation_failed')); return }
      await onRefresh?.()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 8, padding: 8 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <Workflow size={12} style={{ color: 'var(--text-muted)' }} />
        <span style={sectionLabel}>{t('ui6OrgsMore.automations')}</span>
        <span style={{ flex: 1 }} />
        <button style={{ ...smallBtn, padding: 3 }} onClick={onRefresh} aria-label={t('ui6OrgsMore.refresh_automations')}><RefreshCw size={10} /></button>
      </div>
      <div style={{ ...mutedText, fontSize: 10 }}>{t('ui6OrgsMore.drag_help')}</div>

      {loading && <div style={{ display: 'flex', justifyContent: 'center', padding: 8 }}><div className="spinner" /></div>}
      {!loading && error && <div style={{ ...mono, fontSize: 10.5, color: '#f87171' }}>{error}</div>}
      {!loading && !error && automations.length === 0 && (
        <div style={{ ...mono, fontSize: 10.5, color: 'var(--text-muted)' }}>{t('ui6OrgsMore.no_automations')}</div>
      )}

      {automations.map(a => (
        <div
          key={a.alias}
          data-testid="org-automation"
          onMouseDown={(e) => { if (e.button === 0 && !e.target.closest('button')) { e.preventDefault(); onDragStart?.(a, e) } }}
          title={t('ui6OrgsMore.drag_title')}
          style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '6px 6px', border: '1px solid var(--border)', borderRadius: 'var(--radius)', background: 'var(--surface)', cursor: 'grab' }}
        >
          <GripVertical size={11} style={{ color: 'var(--text-muted)', flexShrink: 0 }} />
          <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', gap: 3 }}>
            <span style={{ ...mono, fontSize: 11, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.workflow_name || a.alias}</span>
            <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
              <Chip>{a.alias}</Chip>
              {a.has_outbound_nodes && (
                <Chip color="var(--red, #ef4444)" title={t('ui6OrgsMore.sends_outside_nodes', { nodes: (a.outbound_nodes || []).join(', ') || t('ui6OrgsMore.outbound_nodes') })}>
                  <Send size={8} /> {t('ui6OrgsMore.outbound')}
                </Chip>
              )}
              {a.exists === false && <Chip color="#eab308" title={t('ui6OrgsMore.workflow_deleted')}><AlertTriangle size={8} /> {t('ui6OrgsMore.missing')}</Chip>}
            </div>
          </div>
          <button style={{ ...smallBtn, border: 'none', padding: 3 }} disabled={a.exists === false} onClick={() => onOpenWorkflow?.(a.workflow_id)} aria-label={t('ui6OrgsMore.open_workflow_named', { alias: a.alias })} title={t('ui6OrgsMore.open_workflow')}>
            <ExternalLink size={11} />
          </button>
          <button style={{ ...smallBtn, border: 'none', padding: 3 }} disabled={busy} onClick={() => remove(a)} aria-label={t('ui6OrgsMore.remove_named', { alias: a.alias })} title={t('ui6OrgsMore.remove_from_org')}>
            <Trash2 size={11} />
          </button>
        </div>
      ))}

      <button style={{ ...smallBtn, justifyContent: 'center' }} onClick={toggleLibrary} aria-expanded={libraryOpen}>
        <Plus size={10} /> {libraryOpen ? t('ui6OrgsMore.hide_library') : t('ui6OrgsMore.add_from_library')}
      </button>
      {libraryOpen && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={sectionLabel}>{t('ui6OrgsMore.unassigned')}</span>
          {library === null && <div style={{ display: 'flex', justifyContent: 'center', padding: 6 }}><div className="spinner" /></div>}
          {libraryError && <div style={{ ...mono, fontSize: 10.5, color: '#f87171' }}>{libraryError}</div>}
          {library && !libraryError && library.length === 0 && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <div style={mutedText}>{t('ui6OrgsMore.no_unassigned')}</div>
              {onCreateWorkflow && (
                <button style={{ ...smallBtn, justifyContent: 'center' }} onClick={onCreateWorkflow}>
                  <ExternalLink size={10} /> {t('ui6OrgsMore.open_workflows')}
                </button>
              )}
            </div>
          )}
          {(library || []).map(wf => <LibraryRow key={wf.id} wf={wf} busy={busy} onAdd={add} />)}
        </div>
      )}
    </div>
  )
}

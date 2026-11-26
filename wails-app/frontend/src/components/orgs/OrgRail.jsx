import { ChevronLeft, ChevronRight, Plus, Boxes } from 'lucide-react'
import { OrgTemplatesButton } from './OrgLibraryActions.jsx'
import { Badge } from './ui.jsx'
import { StatusDot, OpenAsBubbleButton, panelFoldBtnStyle, panelStripStyle } from './orgsPanelParts.jsx'
import { useTranslation } from 'react-i18next'

// The org list rail (fold button, new-org form, template button, rows).
export default function OrgRail({
  embedded, orgListOpen, setOrgListOpen, creatingOrg, setCreatingOrg,
  newOrgName, setNewOrgName, newOrgGoal, setNewOrgGoal, createBusy, createError, setCreateError,
  handleCreateOrg, loadOrgs, selectOrg, loadingOrgs, orgs, selected, needsYouCounts,
}) {
  const { t } = useTranslation()
  return (
        orgListOpen ? (
        <div style={{ width: embedded ? 130 : 220, flexShrink: 0, borderRight: '1px solid var(--border)', overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
          <button onClick={() => setOrgListOpen(false)} title={t('ui6Orgs.collapse_list')} style={panelFoldBtnStyle}>
            <ChevronLeft size={11} /> {t('ui6Orgs.collapse')}
          </button>
          <div style={{ flex: 1, overflowY: 'auto', padding: 8, display: 'flex', flexDirection: 'column', gap: 8 }}>
          {!creatingOrg ? (
            <button
              onClick={() => setCreatingOrg(true)}
              style={{
                display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 4,
                fontFamily: 'var(--font-mono)', fontSize: 10.5, padding: '6px 8px', borderRadius: 'var(--radius)',
                background: 'var(--elevated)', border: '1px solid var(--border-active)', color: 'var(--text)', cursor: 'pointer',
              }}
            >
              <Plus size={12} /> {t('ui6Orgs.new_org')}
            </button>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, padding: 8, background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)' }}>
              <input
                autoFocus
                placeholder={t('ui6Orgs.name_placeholder')}
                value={newOrgName}
                onChange={e => setNewOrgName(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') handleCreateOrg(); if (e.key === 'Escape') setCreatingOrg(false) }}
                style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, padding: '5px 6px', background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 4, color: 'var(--text)' }}
              />
              <input
                placeholder={t('ui6Orgs.goal_placeholder')}
                value={newOrgGoal}
                onChange={e => setNewOrgGoal(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') handleCreateOrg(); if (e.key === 'Escape') setCreatingOrg(false) }}
                style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, padding: '5px 6px', background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 4, color: 'var(--text)' }}
              />
              {createError && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: '#f87171' }}>{createError}</div>}
              <div style={{ display: 'flex', gap: 6 }}>
                <button onClick={handleCreateOrg} disabled={createBusy} style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 6px', borderRadius: 4, background: 'var(--elevated)', border: '1px solid var(--border-active)', color: 'var(--text)', cursor: createBusy ? 'default' : 'pointer' }}>
                  {createBusy ? t('ui6Orgs.creating') : t('ui6Orgs.create')}
                </button>
                <button onClick={() => { setCreatingOrg(false); setCreateError('') }} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 4, background: 'transparent', border: '1px solid var(--border)', color: 'var(--text-muted)', cursor: 'pointer' }}>
                  {t('ui6Orgs.cancel')}
                </button>
              </div>
            </div>
          )}
          <OrgTemplatesButton onInstalled={res => { loadOrgs(true).then(() => { if (res?.local_id) selectOrg(res.local_id, { tab: 'design' }) }) }} />
          {loadingOrgs ? (
            <div style={{ display: 'flex', justifyContent: 'center', padding: 16 }}><div className="spinner" /></div>
          ) : orgs.length === 0 ? (
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', padding: 8, textAlign: 'center' }}>{t('ui6Orgs.no_orgs')}</div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {orgs.map(o => (
                <div key={o.name} className="org-list-row" style={{ display: 'flex', alignItems: 'center' }}>
                <button
                  onClick={() => selectOrg(o.name)}
                  style={{
                    flex: 1, minWidth: 0,
                    display: 'flex', alignItems: 'center', gap: 6, textAlign: 'left',
                    background: selected === o.name ? 'var(--elevated)' : 'transparent',
                    border: selected === o.name ? '1px solid var(--border-active)' : '1px solid transparent',
                    borderRadius: 'var(--radius)', padding: '6px 8px', cursor: 'pointer',
                  }}
                >
                  <StatusDot status={o.status} />
                  <span style={{ flex: 1, minWidth: 0, fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{o.name}</span>
                  {o.kind === 'holding' && <Boxes size={10} style={{ color: 'var(--text-muted)', flexShrink: 0 }} aria-label={t('ui6Orgs.holding_org')} />}
                  <Badge count={needsYouCounts[o.name] || 0} style={{ flexShrink: 0 }} />
                </button>
                <OpenAsBubbleButton orgName={o.name} compact />
                </div>
              ))}
            </div>
          )}
          </div>
        </div>
        ) : (
          <button
            onClick={() => setOrgListOpen(true)}
            title={t('ui6Orgs.show_list')}
            style={panelStripStyle('right')}
          >
            <ChevronRight size={13} />
          </button>
        )
  )
}

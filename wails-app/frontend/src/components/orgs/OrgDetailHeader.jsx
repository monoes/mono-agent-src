import { useTranslation } from 'react-i18next'
import { Building2, Play, Loader2, Maximize2 } from 'lucide-react'
import { PublishOrgButton } from './OrgLibraryActions.jsx'
import AutonomyBar from './AutonomyBar.jsx'
import OrgSignatureBanner from './OrgSignatureBanner.jsx'
import { Chip } from './ui.jsx'
import { TABS, OpenAsBubbleButton } from './orgsPanelParts.jsx'

// Detail header: org name row, signature banner, tab strip, run prompt and the
// shared run picker.
export default function OrgDetailHeader({
  selected, tab, setTab, designerFullscreen, setDesignerFullscreen, isHolding, onNavigate, openJevSettings,
  needsYouCounts, queuedCount, runningOrgs, runPromptOpen, setRunPromptOpen, runTaskText, setRunTaskText,
  handleRunOrg, runsList, selectedRun, setSelectedRun,
}) {
  const { t } = useTranslation()
  return (
    <>
              {!(designerFullscreen && tab === 'design') && (
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '6px 10px', borderBottom: '1px solid var(--border)', flexWrap: 'wrap', flexShrink: 0 }}>
                <Building2 size={12} style={{ color: 'var(--text-muted)' }} />
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11.5, fontWeight: 600, color: 'var(--text)' }}>{selected}</span>
                {isHolding && <Chip>{t('ui6Orgs.holding')}</Chip>}
                <PublishOrgButton orgName={selected} />
                <OpenAsBubbleButton orgName={selected} />
                {/* flex-basis 0 keeps the bar beside the name and lets it wrap
                    its own second line (the jev note) instead of the header's. */}
                <div style={{ flex: '1 1 0', display: 'flex', justifyContent: 'flex-end' }}>
                  <AutonomyBar orgName={selected} onOpenJevSettings={onNavigate ? openJevSettings : undefined} />
                </div>
              </div>
              )}
              {/* The designer shows its own, refreshed with each edit. */}
              {tab !== 'design' && <OrgSignatureBanner orgName={selected} />}
              {!(designerFullscreen && tab === 'design') && (
              <div style={{ display: 'flex', gap: 4, padding: '8px', borderBottom: '1px solid var(--border)', overflowX: 'auto', flexShrink: 0 }}>
                {TABS.filter(tabDef => !tabDef.holdingOnly || isHolding).map(tabDef => {
                  const Icon = tabDef.icon
                  const active = tab === tabDef.id
                  const badgeCount = tabDef.id === 'needs' ? (needsYouCounts[selected] || 0)
                    : tabDef.id === 'queued' ? queuedCount : 0
                  // Needs you is waiting on the person; a queued message
                  // only waits for the org to start, so it is not red.
                  const badgeBg = tabDef.id === 'queued' ? 'var(--text-muted)' : 'var(--red, #ef4444)'
                  const badgeFg = tabDef.id === 'queued' ? 'var(--surface, #060b13)' : '#fff'
                  return (
                    <button
                      key={tabDef.id}
                      onClick={() => setTab(tabDef.id)}
                      style={{
                        position: 'relative',
                        display: 'flex', alignItems: 'center', gap: 4, whiteSpace: 'nowrap',
                        fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 'var(--radius)',
                        background: active ? 'var(--elevated)' : 'transparent',
                        border: active ? '1px solid var(--border-active)' : '1px solid transparent',
                        color: active ? 'var(--text)' : 'var(--text-muted)', cursor: 'pointer',
                      }}
                    >
                      <Icon size={11} /> {t(tabDef.labelKey)}
                      {badgeCount > 0 && (
                        <span style={{
                          position: 'absolute', top: -5, right: -5,
                          minWidth: 14, height: 14, padding: '0 3px', borderRadius: 7,
                          background: badgeBg, color: badgeFg,
                          fontFamily: 'var(--font-mono)', fontSize: 9, fontWeight: 700, lineHeight: '14px', textAlign: 'center',
                          boxShadow: '0 0 0 2px var(--surface, #060b13)',
                        }}>
                          {badgeCount > 99 ? '99+' : badgeCount}
                        </span>
                      )}
                    </button>
                  )
                })}
                {runningOrgs.has(selected) ? (
                  <button
                    disabled
                    style={{
                      marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 4,
                      fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 'var(--radius)',
                      background: 'transparent', border: '1px solid var(--border)', color: 'var(--text-muted)', cursor: 'default',
                    }}
                  >
                    <Loader2 size={11} style={{ animation: 'spin 0.9s linear infinite' }} /> {t('orgs.tabBar.running')}
                  </button>
                ) : (
                  <button
                    onClick={() => setRunPromptOpen(v => !v)}
                    title={t('orgs.tabBar.runTitle')}
                    style={{
                      marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 4,
                      fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 'var(--radius)',
                      background: runPromptOpen ? 'var(--elevated)' : 'transparent',
                      border: runPromptOpen ? '1px solid var(--border-active)' : '1px solid var(--border)',
                      color: 'var(--text)', cursor: 'pointer',
                    }}
                  >
                    <Play size={11} /> {t('orgs.tabBar.run')}
                  </button>
                )}
                {tab === 'design' && (
                  <button
                    onClick={() => setDesignerFullscreen(v => !v)}
                    title={designerFullscreen ? t('orgs.tabBar.exitFullscreen') : t('orgs.tabBar.fullscreen')}
                    style={{
                      display: 'flex', alignItems: 'center', gap: 4,
                      fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 'var(--radius)',
                      background: 'transparent', border: '1px solid transparent', color: 'var(--text-muted)', cursor: 'pointer',
                    }}
                  >
                    <Maximize2 size={11} />
                  </button>
                )}
              </div>
              )}

              {runPromptOpen && (
                <div style={{ display: 'flex', gap: 6, padding: '6px 8px', borderBottom: '1px solid var(--border)', flexShrink: 0 }}>
                  <input
                    autoFocus
                    type="text"
                    value={runTaskText}
                    onChange={e => setRunTaskText(e.target.value)}
                    onKeyDown={e => { if (e.key === 'Enter') handleRunOrg(); if (e.key === 'Escape') { setRunPromptOpen(false); setRunTaskText('') } }}
                    placeholder={t('ui6Orgs.run_message_placeholder')}
                    style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 11, background: 'var(--elevated)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '6px 8px', outline: 'none' }}
                  />
                  <button className="btn btn-primary btn-sm" onClick={handleRunOrg}>
                    <Play size={11} /> {t('orgs.tabBar.run')}
                  </button>
                </div>
              )}

              {!['design', 'needs', 'queued', 'group'].includes(tab) && (runningOrgs.has(selected) || runsList.length > 0) && (
                // Shared run context — which run every tab (not just
                // Overview) is scoped to. Logs/Costs/Flow/Decisions all read
                // selectedRun too now, so this needs to be visible no matter
                // which tab is open, not buried inside Overview's own body.
                <div style={{ display: 'flex', flexDirection: 'column', gap: 8, padding: '8px 12px', borderBottom: '1px solid var(--border)', flexShrink: 0 }}>
                  {runningOrgs.has(selected) && (
                    <div style={{
                      display: 'flex', alignItems: 'center', gap: 6, padding: '6px 10px',
                      background: 'rgba(0,245,212,0.06)', border: '1px solid rgba(0,245,212,0.2)', borderRadius: 'var(--radius)',
                    }}>
                      <span className="live-dot" />
                      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--teal, #00f5d4)', fontWeight: 600, letterSpacing: 0.5 }}>{t('ui6Orgs.org_running')}</span>
                    </div>
                  )}
                  {runsList.length > 0 && (
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>{t('ui6Orgs.run_noun')}</span>
                      <select
                        value={selectedRun || ''}
                        onChange={e => setSelectedRun(e.target.value || null)}
                        className="filter-select"
                        style={{ flex: 1, maxWidth: 360 }}
                      >
                        {runsList.some(r => r.live) && <option value="live">{t('ui6Orgs.run_live_option')}</option>}
                        {runsList.filter(r => !r.live).map(r => {
                          const outcome = r.summary?.outcome?.status
                          return <option key={r.run} value={r.run}>{r.run}{outcome ? ` — ${t(`ui6Orgs.outcome_${outcome}`, { defaultValue: outcome })}` : ''}</option>
                        })}
                      </select>
                    </div>
                  )}
                </div>
              )}
    </>
  )
}

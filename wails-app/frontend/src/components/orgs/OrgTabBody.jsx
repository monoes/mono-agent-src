import { useTranslation } from 'react-i18next'
import { MessageCircle } from 'lucide-react'
import { KVBlock } from '../KVBlock.jsx'
import { emitOpenOrgBubble } from '../../lib/appEvents.js'
import DocumentsPanel from '../orgdesigner/DocumentsPanel.jsx'
import ScheduleAuditPanel from '../orgdesigner/ScheduleAuditPanel.jsx'
import GroupView from './GroupView.jsx'
import QueuedMessagesPanel from './QueuedMessagesPanel.jsx'
import NeedsYouPanel from './NeedsYouPanel.jsx'
import DecisionsFeed from './DecisionsFeed.jsx'
import FullAccessSummary from './FullAccessSummary.jsx'
import OrgToolActivity from './OrgToolActivity.jsx'
import SectionBudgetPanel from './SectionBudgetPanel.jsx'
import { isToolActivity } from './orgToolActivity.js'
import { Card, OutcomeBanner, itemsOf, extractFinalMessage } from './orgsPanelParts.jsx'

// The generic (non-design) tab body of the org detail pane.
export default function OrgTabBody({
  selected, tab, tabLoading, data, events, runsList, selectedRun, runDetail, needsYouCounts,
  handleNeedsYouCount, runningOrgs, handleRunOrg, setQueuedCount, isHolding, selectOrg, loadTab,
}) {
  const { t } = useTranslation()
  return (
              <div style={{ flex: 1, overflowY: 'auto', padding: 12, display: 'flex', flexDirection: 'column', gap: 10 }}>
                {tabLoading && <div style={{ display: 'flex', justifyContent: 'center', padding: 8 }}><div className="spinner" /></div>}

                {!tabLoading && tab === 'overview' && (
                  <>
                    <FullAccessSummary orgName={selected} status={data.overview} onChanged={() => loadTab(selected, 'overview')} />
                    {!selectedRun ? (
                      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No completed runs yet.</div>
                    ) : selectedRun === 'live' ? (
                      <>
                        <OutcomeBanner
                          outcome={runsList.find(r => r.live)?.summary?.outcome}
                          finalMessage={extractFinalMessage(events, runsList.find(r => r.live)?.summary?.outcome)}
                        />
                        <SectionBudgetPanel orgName={selected} live />
                        <details>
                          <summary style={{ cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>Raw status</summary>
                          <Card><KVBlock obj={data.overview} /></Card>
                        </details>
                        <div>
                          <OrgToolActivity events={events} isLive runKey="live" />
                          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 6 }}>Live events</div>
                          <div style={{ display: 'flex', flexDirection: 'column', gap: 4, maxHeight: 260, overflowY: 'auto' }}>
                            {events.length === 0 && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>Waiting for events…</div>}
                            {events.filter(e => !isToolActivity(e)).map((e, i) => (
                              <div key={e.id ?? `${e.ts ?? ''}-${i}`} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '4px 8px', overflowWrap: 'anywhere' }}>
                                <span style={{ color: 'var(--cyan)' }}>{e.type}</span>{e.from ? ` ${e.from}→${e.to || '?'}` : ''}{e.subject ? `: ${e.subject}` : ''}{e.msg ? ` ${e.msg}` : ''}
                              </div>
                            ))}
                          </div>
                        </div>
                      </>
                    ) : (
                      // A specific historical run is selected — its own
                      // static report + logs, not the live tail above.
                      runDetail?.loading || runDetail?.run !== selectedRun ? (
                        <div style={{ display: 'flex', justifyContent: 'center', padding: 16 }}><div className="spinner" /></div>
                      ) : (
                        <>
                          <OutcomeBanner
                            outcome={runDetail.report?.outcome}
                            finalMessage={extractFinalMessage(runDetail.logs, runDetail.report?.outcome)}
                          />
                          <SectionBudgetPanel orgName={selected} run={selectedRun} />
                          <details>
                            <summary style={{ cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>Raw report</summary>
                            <Card><KVBlock obj={runDetail.report} /></Card>
                          </details>
                          <div>
                            <OrgToolActivity events={itemsOf(runDetail.logs)} runKey={selectedRun} />
                            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 6 }}>Events</div>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: 4, maxHeight: 260, overflowY: 'auto' }}>
                              {itemsOf(runDetail.logs).length === 0 && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>No events recorded.</div>}
                              {itemsOf(runDetail.logs).filter(e => !isToolActivity(e)).map((e, i) => (
                                <div key={e.id ?? `${e.ts ?? ''}-${i}`} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '4px 8px', overflowWrap: 'anywhere' }}>
                                  <span style={{ color: 'var(--cyan)' }}>{e.type}</span>{e.from ? ` ${e.from}→${e.to || '?'}` : ''}{e.subject ? `: ${e.subject}` : ''}{e.msg ? ` ${e.msg}` : ''}
                                </div>
                              ))}
                            </div>
                          </div>
                        </>
                      )
                    )}
                  </>
                )}

                {tab === 'needs' && (
                  <>
                    {(needsYouCounts[selected] || 0) > 0 && (
                      <button type="button" className="btn btn-primary btn-sm" data-testid="org-answer-in-bubble"
                        onClick={() => emitOpenOrgBubble(selected)} aria-label={t('orgBubble.answerInBubbleNamed', { name: selected })}
                        style={{ gap: 4, fontSize: 10, marginBottom: 8 }}>
                        <MessageCircle size={12} /> {t('orgBubble.answerInBubble')}
                      </button>
                    )}
                    <NeedsYouPanel orgName={selected} onCountChange={handleNeedsYouCount} />
                  </>
                )}

                {tab === 'queued' && (
                  <QueuedMessagesPanel orgName={selected} running={runningOrgs.has(selected)} onStartOrg={handleRunOrg} onCountChange={setQueuedCount} />
                )}

                {tab === 'decisions' && (
                  <DecisionsFeed orgName={selected} run={selectedRun && selectedRun !== 'live' ? selectedRun : ''} />
                )}

                {tab === 'documents' && (
                  <DocumentsPanel orgName={selected} run={selectedRun && selectedRun !== 'live' ? selectedRun : ''} live={selectedRun === 'live'} />
                )}

                {tab === 'group' && isHolding && (
                  <GroupView holding={selected} onOpenOrg={(name) => selectOrg(name)} />
                )}

                {tab === 'logs' && <ScheduleAuditPanel orgName={selected} live={selectedRun === 'live'} />}

                {!tabLoading && tab === 'logs' && (
                  itemsOf(data.logs).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No log entries.</div>
                    : <>
                      <OrgToolActivity events={itemsOf(data.logs)} isLive={selectedRun === 'live'} runKey={`logs-${selectedRun}`} />
                      {itemsOf(data.logs).filter(e => !isToolActivity(e)).map((e, i) => (
                      <div key={e.id ?? `${e.ts ?? ''}-${i}`} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '4px 8px' }}>
                        <span style={{ color: 'var(--cyan)' }}>{e.type}</span>{e.from ? ` ${e.from}→${e.to || '?'}` : ''}{e.subject ? `: ${e.subject}` : ''}
                      </div>
                    ))}
                    </>
                )}

                {!tabLoading && tab === 'costs' && (
                  <>
                    {itemsOf(data.costs).length === 0
                      ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No cost data.</div>
                      : itemsOf(data.costs).map((c, i) => (
                        <Card key={i}><KVBlock obj={c} /></Card>
                      ))}
                    {data.costs?.totals && <Card><KVBlock obj={data.costs.totals} /></Card>}
                  </>
                )}

                {!tabLoading && tab === 'flow' && (
                  itemsOf(data.flow?.edges).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No flow data.</div>
                    : itemsOf(data.flow?.edges).map((e, i) => (
                      <div key={i} style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '6px 10px' }}>
                        {e.from} → {e.to}{e.subject ? `: ${e.subject}` : ''}
                      </div>
                    ))
                )}

                {!tabLoading && tab === 'trace' && (
                  itemsOf(data.trace).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No decision trace recorded.</div>
                    : itemsOf(data.trace).map((d, i) => (
                      <Card key={i}><KVBlock obj={d} /></Card>
                    ))
                )}
              </div>
  )
}

import { useState, useEffect, useCallback, useRef } from 'react'
import { CheckCircle, RefreshCw, AlertTriangle, UserCheck, X } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { api, notify } from '../services/api.js'
import { usePageVisibleRef, useVisibleCatchUp } from '../lib/usePageVisible.js'
import { emitCommsChanged } from '../lib/commsStatus.js'
import { PersonItemsInbox } from '../components/bubbles/PersonItemViews.jsx'
import { usePersonItemsContext } from '../components/bubbles/PersonItemsContext.jsx'

const GetHILItems          = WailsApp.GetHILItems          ?? (async () => [])
const ApproveHIL           = WailsApp.ApproveHIL           ?? (async () => {})
const RejectHIL            = WailsApp.RejectHIL            ?? (async () => {})
const GetDraftPersonMessages   = WailsApp.GetDraftPersonMessages   ?? (async () => [])
const SendDraftPersonMessage   = WailsApp.SendDraftPersonMessage   ?? (async () => {})
const RejectDraftPersonMessage = WailsApp.RejectDraftPersonMessage ?? (async () => {})

import { HILCard, DraftMessageCard, suggestionOf, sortBySuggestion, SuggestionChip } from './hil/HILCards.jsx'
import { LeadApprovalCard } from './hil/LeadApprovalCard.jsx'
import { OrgDecisionCard } from './hil/OrgDecisionCard.jsx'
import { HilScope } from './hil/AllProfiles.jsx'

export { suggestionOf, sortBySuggestion, SuggestionChip }

// ── Main HumanInLoop component ────────────────────────────────
export default function HumanInLoop({ embedded = false, isOpen = true, onClose, onPendingCountChange, onProfile, onNavigate }) {
  const [items, setItems] = useState([])
  const [drafts, setDrafts] = useState([])
  const [leads, setLeads] = useState([])
  const [orgItems, setOrgItems] = useState([])
  const [activeFilter, setActiveFilter] = useState('all') // 'all' | 'leads' | 'workflows' | 'drafts' | 'orgs'
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const pageVisibleRef = usePageVisibleRef()
  // The items that wait for the person (a giant plan): one poll, held by App.
  const { items: personItems } = usePersonItemsContext()
  const baseTotal = useRef(0)
  const personCount = useRef(0)
  personCount.current = personItems.length

  const loadInFlight = useRef(false)
  const load = useCallback(async (background = false) => {
    // A slow poll must not stack: a background tick waits for the one running.
    if (background && loadInFlight.current) return
    loadInFlight.current = true
    if (!background) setLoading(true)
    setError(null)
    try {
      const [hilRes, draftRes, leadRes, orgsRes] = await Promise.allSettled([
        GetHILItems(),
        GetDraftPersonMessages(),
        api.getPendingPeopleApprovals(),
        api.listOrgDesigns(),
      ])

      const newHil = (hilRes.status === 'fulfilled' && Array.isArray(hilRes.value)) ? hilRes.value : []
      const newDrafts = (draftRes.status === 'fulfilled' && Array.isArray(draftRes.value)) ? draftRes.value : []
      const newLeads = (leadRes.status === 'fulfilled' && Array.isArray(leadRes.value)) ? leadRes.value : []

      let newOrgItems = []
      if (orgsRes.status === 'fulfilled' && orgsRes.value) {
        const orgList = Array.isArray(orgsRes.value.items)
          ? orgsRes.value.items
          : (Array.isArray(orgsRes.value) ? orgsRes.value : [])
        const orgPromises = orgList.map(async (org) => {
          const orgName = org.name
          if (!orgName) return []
          try {
            const [qRes, aRes, gRes] = await Promise.allSettled([
              api.getOrgQuestions(orgName),
              api.getOrgApprovals(orgName),
              api.getOrgGates(orgName),
            ])
            const questions = qRes.status === 'fulfilled' ? (qRes.value?.questions || []) : []
            const approvals = aRes.status === 'fulfilled' ? (aRes.value?.approvals || []) : []
            const gates = gRes.status === 'fulfilled' ? (gRes.value?.gates || []) : []

            const res = []
            for (const q of questions) {
              if (q.answer == null && !q.answeredAt) {
                res.push({ kind: 'question', id: q.questionId, orgName, role: q.role, question: q.question, text: q.question, raw: q })
              }
            }
            for (const a of approvals) {
              if (a.approved == null) {
                res.push({ kind: 'approval', id: a.requestId || `${a.roleId}:${a.action}`, orgName, role: a.roleId, action: a.action, summary: a.question || `Approve ${a.action}?`, text: a.question || `Approve ${a.action}?`, raw: a })
              }
            }
            for (const g of gates) {
              if (g.status === 'pending') {
                res.push({ kind: 'gate', id: g.id, orgName, role: g.roleId, name: g.name, text: g.name || g.id, raw: g })
              }
            }
            return res
          } catch {
            return []
          }
        })
        const orgResults = await Promise.all(orgPromises)
        newOrgItems = orgResults.flat()
      }

      setItems(sortBySuggestion(newHil))
      setDrafts(newDrafts)
      setLeads(newLeads)
      setOrgItems(newOrgItems)

      baseTotal.current = newHil.length + newDrafts.length + newLeads.length + newOrgItems.length
      onPendingCountChange?.(baseTotal.current + personCount.current)
    } catch (e) {
      setError(e?.message ?? 'Failed to load')
    } finally {
      loadInFlight.current = false
      if (!background) setLoading(false)
    }
  }, [onPendingCountChange])

  useVisibleCatchUp(() => load(true))
  useEffect(() => { onPendingCountChange?.(baseTotal.current + personItems.length) }, [personItems.length, onPendingCountChange])

  useEffect(() => {
    if (embedded && !isOpen) return
    load()
    const interval = setInterval(() => {
      if (!pageVisibleRef.current) return
      load(true)
    }, 4000)
    return () => clearInterval(interval)
  }, [load, pageVisibleRef, embedded, isOpen])

  // Workflow HIL resolution
  const handleApproveHIL = async (id, editedJSON) => {
    try {
      await ApproveHIL(id, editedJSON)
      emitCommsChanged()
      setItems(prev => prev.filter(i => i.id !== id))
      notify('workflow', 'Workflow resumed')
    } catch (e) {
      setError(e?.message ?? 'Failed to approve item')
    }
  }

  const handleRejectHIL = async (id) => {
    try {
      await RejectHIL(id)
      emitCommsChanged()
      setItems(prev => prev.filter(i => i.id !== id))
      notify('workflow', 'Workflow branch rejected')
    } catch (e) {
      setError(e?.message ?? 'Failed to reject item')
    }
  }

  // Draft Message resolution
  const handleSendDraft = async (id) => {
    try {
      await SendDraftPersonMessage(id)
      emitCommsChanged()
      setDrafts(prev => prev.filter(d => d.id !== id))
      notify('message', 'Draft message sent')
    } catch (e) {
      setError(e?.message ?? 'Failed to send draft')
    }
  }

  const handleRejectDraft = async (id) => {
    try {
      await RejectDraftPersonMessage(id)
      emitCommsChanged()
      setDrafts(prev => prev.filter(d => d.id !== id))
      notify('message', 'Draft message discarded')
    } catch (e) {
      setError(e?.message ?? 'Failed to discard draft')
    }
  }

  // Lead outreach resolution
  const handleApproveLead = async (id, editedIntro, sendNow) => {
    try {
      await api.approvePendingPerson(id, editedIntro, sendNow)
      emitCommsChanged()
      setLeads(prev => prev.filter(l => l.id !== id))
      notify('outreach', sendNow ? 'Approved and queued for dispatch' : 'Approved candidate')
    } catch (e) {
      setError(e?.message ?? 'Failed to approve lead')
    }
  }

  const handleRejectLead = async (id) => {
    try {
      await api.rejectPendingPerson(id)
      emitCommsChanged()
      setLeads(prev => prev.filter(l => l.id !== id))
      notify('outreach', 'Candidate rejected')
    } catch (e) {
      setError(e?.message ?? 'Failed to reject lead')
    }
  }

  // Org resolution
  const handleAnswerQuestion = async (orgName, questionId, text) => {
    try {
      await api.answerOrgQuestion(orgName, questionId, text)
      setOrgItems(prev => prev.filter(i => !(i.orgName === orgName && i.id === questionId)))
      notify('org', 'Question answered')
    } catch (e) {
      setError(e?.message ?? 'Failed to answer question')
    }
  }

  const handleApproveAction = async (orgName, role, action) => {
    try {
      await api.approveOrgAction(orgName, role, action)
      setOrgItems(prev => prev.filter(i => !(i.orgName === orgName && i.role === role && i.action === action)))
      notify('org', `Approved ${action}`)
    } catch (e) {
      setError(e?.message ?? 'Failed to approve action')
    }
  }

  const handleDenyAction = async (orgName, role, action) => {
    try {
      await api.denyOrgAction(orgName, role, action)
      setOrgItems(prev => prev.filter(i => !(i.orgName === orgName && i.role === role && i.action === action)))
      notify('org', `Denied ${action}`)
    } catch (e) {
      setError(e?.message ?? 'Failed to deny action')
    }
  }

  const handleApproveGate = async (orgName, gateId) => {
    try {
      await api.gateApproveOrgAction(orgName, gateId)
      setOrgItems(prev => prev.filter(i => !(i.orgName === orgName && i.id === gateId)))
      notify('org', 'Gate approved')
    } catch (e) {
      setError(e?.message ?? 'Failed to approve gate')
    }
  }

  const handleRejectGate = async (orgName, gateId) => {
    try {
      await api.gateRejectOrgAction(orgName, gateId)
      setOrgItems(prev => prev.filter(i => !(i.orgName === orgName && i.id === gateId)))
      notify('org', 'Gate rejected')
    } catch (e) {
      setError(e?.message ?? 'Failed to reject gate')
    }
  }

  if (embedded && !isOpen) return null

  const totalCount = items.length + drafts.length + leads.length + orgItems.length + personItems.length

  const showLeads = activeFilter === 'all' || activeFilter === 'leads'
  const showWorkflows = activeFilter === 'all' || activeFilter === 'workflows'
  const showDrafts = activeFilter === 'all' || activeFilter === 'drafts'
  const showOrgs = activeFilter === 'all' || activeFilter === 'orgs'

  const containerStyle = embedded
    ? {
        width: 480,
        maxWidth: '100vw',
        flexShrink: 0,
        background: '#060b13',
        borderLeft: '1px solid rgba(0,180,216,0.15)',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'hidden',
        position: 'relative',
        height: '100%',
      }
    : { padding: '24px 28px', maxWidth: 800, margin: '0 auto', overflowY: 'auto', height: '100%' }

  const bodyStyle = embedded
    ? { flex: 1, overflowY: 'auto', padding: '16px 16px 24px' }
    : undefined

  const body = (
    <>
      {!embedded && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 20 }}>
          <div>
            <h1 style={{ fontSize: 20, fontWeight: 700, color: '#e2e8f0', margin: 0 }}>Human in Loop</h1>
            <p style={{ fontSize: 13, color: 'var(--text-muted)', margin: '4px 0 0' }}>
              Central queue for all pending human reviews, lead outreach approvals, workflow pauses, and agent decisions.
            </p>
          </div>
          <button
            onClick={() => load()}
            disabled={loading}
            style={{
              marginLeft: 'auto',
              display: 'flex', alignItems: 'center', gap: 6,
              padding: '7px 14px',
              background: 'rgba(255,255,255,0.06)',
              border: '1px solid rgba(255,255,255,0.1)',
              borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
              color: 'var(--text-secondary)', fontSize: 13,
            }}
          >
            <RefreshCw size={13} style={{ animation: loading ? 'spin 1s linear infinite' : 'none' }} />
            Refresh
          </button>
        </div>
      )}

      <HilScope>
      <PersonItemsInbox />

      {/* Filter Tabs / Pills */}
      {totalCount > 0 && (
        <div style={{ display: 'flex', gap: 6, marginBottom: 16, overflowX: 'auto', paddingBottom: 4 }}>
          <button
            onClick={() => setActiveFilter('all')}
            style={{
              padding: '4px 10px', borderRadius: 20,
              fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600,
              background: activeFilter === 'all' ? 'rgba(0,180,216,0.2)' : 'rgba(255,255,255,0.04)',
              border: activeFilter === 'all' ? '1px solid rgba(0,180,216,0.4)' : '1px solid rgba(255,255,255,0.08)',
              color: activeFilter === 'all' ? '#00b4d8' : 'var(--text-muted)',
              cursor: 'pointer',
            }}
          >
            All ({totalCount})
          </button>
          {leads.length > 0 && (
            <button
              onClick={() => setActiveFilter('leads')}
              style={{
                padding: '4px 10px', borderRadius: 20,
                fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600,
                background: activeFilter === 'leads' ? 'rgba(0,180,216,0.2)' : 'rgba(255,255,255,0.04)',
                border: activeFilter === 'leads' ? '1px solid rgba(0,180,216,0.4)' : '1px solid rgba(255,255,255,0.08)',
                color: activeFilter === 'leads' ? '#00b4d8' : 'var(--text-muted)',
                cursor: 'pointer',
              }}
            >
              Leads ({leads.length})
            </button>
          )}
          {items.length > 0 && (
            <button
              onClick={() => setActiveFilter('workflows')}
              style={{
                padding: '4px 10px', borderRadius: 20,
                fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600,
                background: activeFilter === 'workflows' ? 'rgba(0,180,216,0.2)' : 'rgba(255,255,255,0.04)',
                border: activeFilter === 'workflows' ? '1px solid rgba(0,180,216,0.4)' : '1px solid rgba(255,255,255,0.08)',
                color: activeFilter === 'workflows' ? '#00b4d8' : 'var(--text-muted)',
                cursor: 'pointer',
              }}
            >
              Workflows ({items.length})
            </button>
          )}
          {drafts.length > 0 && (
            <button
              onClick={() => setActiveFilter('drafts')}
              style={{
                padding: '4px 10px', borderRadius: 20,
                fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600,
                background: activeFilter === 'drafts' ? 'rgba(245,158,11,0.2)' : 'rgba(255,255,255,0.04)',
                border: activeFilter === 'drafts' ? '1px solid rgba(245,158,11,0.4)' : '1px solid rgba(255,255,255,0.08)',
                color: activeFilter === 'drafts' ? '#f59e0b' : 'var(--text-muted)',
                cursor: 'pointer',
              }}
            >
              Messages ({drafts.length})
            </button>
          )}
          {orgItems.length > 0 && (
            <button
              onClick={() => setActiveFilter('orgs')}
              style={{
                padding: '4px 10px', borderRadius: 20,
                fontSize: 11, fontFamily: 'var(--font-mono)', fontWeight: 600,
                background: activeFilter === 'orgs' ? 'rgba(139,92,246,0.2)' : 'rgba(255,255,255,0.04)',
                border: activeFilter === 'orgs' ? '1px solid rgba(139,92,246,0.4)' : '1px solid rgba(255,255,255,0.08)',
                color: activeFilter === 'orgs' ? '#a78bfa' : 'var(--text-muted)',
                cursor: 'pointer',
              }}
            >
              Orgs ({orgItems.length})
            </button>
          )}
        </div>
      )}

      {error && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '10px 14px', background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 8, marginBottom: 16, color: '#ef4444', fontSize: 13 }}>
          <AlertTriangle size={14} /> {error}
        </div>
      )}

      {!loading && totalCount === 0 && !error && (
        <div style={{
          textAlign: 'center', padding: '60px 20px',
          border: '1px dashed rgba(255,255,255,0.1)', borderRadius: 12,
          color: 'var(--text-muted)',
        }}>
          <CheckCircle size={32} style={{ marginBottom: 12, opacity: 0.3 }} />
          <div style={{ fontSize: 15, fontWeight: 500, marginBottom: 6 }}>All caught up!</div>
          <div style={{ fontSize: 13 }}>
            No pending reviews across lead campaigns, workflow pauses, outbound drafts, or agent organizations.
          </div>
        </div>
      )}

      {/* Lead Approvals */}
      {showLeads && leads.map(item => (
        <LeadApprovalCard
          key={item.id}
          item={item}
          onApprove={handleApproveLead}
          onReject={handleRejectLead}
          onOpenProfile={onProfile}
        />
      ))}

      {/* Workflow HIL Pauses */}
      {showWorkflows && items.map(item => (
        <HILCard
          key={item.id}
          item={item}
          onApprove={handleApproveHIL}
          onReject={handleRejectHIL}
          onProfile={onProfile}
          onNavigate={onNavigate}
        />
      ))}

      {/* Draft Messages */}
      {showDrafts && drafts.map(item => (
        <DraftMessageCard
          key={item.id}
          item={item}
          onSend={handleSendDraft}
          onReject={handleRejectDraft}
          onProfile={onProfile}
          onNavigate={onNavigate}
        />
      ))}

      {/* Org Decisions */}
      {showOrgs && orgItems.map(item => (
        <OrgDecisionCard
          key={`${item.orgName}-${item.kind}-${item.id}`}
          item={item}
          onAnswerQuestion={handleAnswerQuestion}
          onApproveAction={handleApproveAction}
          onDenyAction={handleDenyAction}
          onApproveGate={handleApproveGate}
          onRejectGate={handleRejectGate}
        />
      ))}
      </HilScope>

      <style>{`@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }`}</style>
    </>
  )

  if (!embedded) return <div style={containerStyle}>{body}</div>

  return (
    <div style={containerStyle}>
      <div style={{ padding: '10px 14px', borderBottom: '1px solid rgba(0,180,216,0.12)', display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0 }}>
        <UserCheck size={14} style={{ color: '#00b4d8' }} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 700, color: '#e2e8f0', flex: 1, letterSpacing: 1 }}>
          HUMAN IN LOOP
        </span>
        {totalCount > 0 && (
          <span style={{
            padding: '1px 6px', borderRadius: 10,
            background: 'rgba(239,68,68,0.2)', color: '#ef4444',
            fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700,
          }}>
            {totalCount}
          </span>
        )}
        <button onClick={() => load()} disabled={loading} title="Refresh" style={{ background: 'transparent', border: 'none', cursor: loading ? 'wait' : 'pointer', color: 'var(--text-muted)', padding: 3, display: 'flex' }}>
          <RefreshCw size={13} style={{ animation: loading ? 'spin 1s linear infinite' : 'none' }} />
        </button>
        {onClose && (
          <button onClick={onClose} title="Close panel" style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 3, display: 'flex' }}>
            <X size={15} />
          </button>
        )}
      </div>
      <div className="hil-body" style={bodyStyle}>{body}</div>
    </div>
  )
}

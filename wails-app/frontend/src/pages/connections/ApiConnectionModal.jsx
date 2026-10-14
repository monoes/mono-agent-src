// Connect / manage one API connection (OAuth, API key, connection string,
// …) from the Connections page's API section. Browser logins are not here:
// they are an automation's Session (AutomationDrawer).
import { useState, useEffect, useCallback, useRef } from 'react'
import { RefreshCw, X, CheckCircle, Loader, Trash2, HelpCircle, ArrowLeft, ExternalLink } from 'lucide-react'
import { api, onConnectionProgress, onConnectionDone } from '../../services/api.js'
import { HELP_GUIDES } from '../../lib/helpGuides.js'
import OAuthFields, { oauthFormFields, missingRequired, oauthFieldArgs } from './OAuthFields.jsx'

function fmtDate(s) {
  if (!s) return '—'
  try { return new Date(s).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' }) }
  catch { return s.slice(0, 10) }
}

// ── HelpModal ─────────────────────────────────────────────────────────────────

function HelpModal({ guide, platformName, emoji, methodLabel, onClose }) {
  return (
    <div
      onClick={e => e.target === e.currentTarget && onClose()}
      style={{ position: 'fixed', inset: 0, background: 'var(--overlay)', zIndex: 1100, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}
    >
      <div role="dialog" aria-modal="true" aria-label={guide.title} style={{ width: '100%', maxWidth: 460, background: 'var(--elevated)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius-xl)', boxShadow: 'var(--shadow-glow)', overflow: 'hidden' }}>

        {/* Header */}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '14px 18px 12px', borderBottom: '1px solid var(--border)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 2, display: 'flex', alignItems: 'center', gap: 4 }}>
              <ArrowLeft size={14} />
            </button>
            <span style={{ fontSize: 18 }}>{emoji}</span>
            <div>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 700, color: 'var(--text)' }}>{guide.title}</div>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, marginTop: 1 }}>
                {platformName} · {methodLabel}
              </div>
            </div>
          </div>
          <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 4, display: 'flex' }}>
            <X size={15} />
          </button>
        </div>

        {/* Steps */}
        <div style={{ padding: '16px 18px 6px', display: 'flex', flexDirection: 'column', gap: 10 }}>
          {guide.steps.map((step, i) => {
            const text = typeof step === 'string' ? step : step.text
            const url  = typeof step === 'string' ? null  : step.url
            return (
              <div key={i} style={{ display: 'flex', gap: 10, alignItems: 'flex-start' }}>
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700, color: 'var(--cyan)', minWidth: 18, lineHeight: '18px', textAlign: 'right' }}>{i + 1}</span>
                <div style={{ flex: 1 }}>
                  <span style={{ fontFamily: 'var(--font-body)', fontSize: 12, color: 'var(--text-secondary)', lineHeight: 1.5 }}>{text}</span>
                  {url && (
                    <button
                      onClick={() => api.openURL(url)}
                      style={{ display: 'inline-flex', alignItems: 'center', gap: 3, marginLeft: 6, background: 'none', border: 'none', cursor: 'pointer', color: 'var(--cyan)', fontFamily: 'var(--font-mono)', fontSize: 9.5, padding: 0, verticalAlign: 'middle' }}
                    >
                      Open <ExternalLink size={9} />
                    </button>
                  )}
                </div>
              </div>
            )
          })}
        </div>

        {/* Primary action */}
        <div style={{ padding: '14px 18px 18px', borderTop: '1px solid var(--border)', marginTop: 10, display: 'flex', gap: 8 }}>
          <button
            className="btn btn-primary btn-sm"
            onClick={() => api.openURL(guide.getKeyURL)}
            style={{ gap: 5, flex: 1, justifyContent: 'center' }}
          >
            <ExternalLink size={11} /> Get credentials →
          </button>
          <button className="btn btn-ghost btn-sm" onClick={onClose}>Done</button>
        </div>
      </div>
    </div>
  )
}

export default function ApiConnectionModal({ platform, conn, onClose, onRefresh, onDisconnect }) {
  const [testing, setTesting]   = useState(false)
  const [testMsg, setTestMsg]   = useState(null)
  const [removing, setRemoving] = useState(false)
  const [confirmDisconnect, setConfirmDisconnect] = useState(false)
  const [saving, setSaving]     = useState(false)
  const [saveErr, setSaveErr]   = useState(null)
  const [saveOk, setSaveOk]     = useState(false)
  const [selMethod, setSelMethod] = useState((platform.methods || []).filter(m => m !== 'browser')[0] || '')
  const [fields, setFields]     = useState({})
  const [flowRunning, setFlowRunning] = useState(false)
  const [flowSteps, setFlowSteps]     = useState([])
  const stepsEndRef = useRef(null)

  const [showHelp, setShowHelp] = useState(false)
  const [oauthCreds, setOauthCreds]         = useState({ clientID: '', clientSecret: '' })
  const [oauthCredsLoaded, setOauthCredsLoaded] = useState(false)
  const [oauthCredsSaved, setOauthCredsSaved]   = useState(false)
  const [oauthCredsNeeded, setOauthCredsNeeded] = useState(false)
  // `connect show --json`, and the OAuth 1.0a form / tenant connect field values.
  const [view, setView] = useState(null)
  const [oauthValues, setOauthValues] = useState({})

  const pid     = platform.id || ''
  const name    = platform.name || pid
  const emoji   = platform.iconEmoji || '🔌'
  // Browser logins belong to the automation's Session tab, not here.
  const methods = (platform.methods || []).filter(m => m !== 'browser')
  const mFields = (platform.fields || {})[selMethod] || []
  const guide   = HELP_GUIDES[pid]?.[selMethod] || null
  const isOAuth = selMethod === 'oauth' || selMethod === 'oauth1'
  const oauthFields = oauthFormFields(view, selMethod)
  const oauthMissing = missingRequired(oauthFields, oauthValues)
  // A connect field client_id (Strava, Facebook Lead Ads) replaces the stored OAuth client.
  const needsClient = selMethod === 'oauth' && !oauthFields.some(f => f.key === 'client_id')
  const canStart = (!needsClient || !!oauthCreds.clientID) && oauthMissing.length === 0

  useEffect(() => { api.showPlatform(pid).then(setView) }, [pid])

  // Load stored OAuth credentials when OAuth method selected
  useEffect(() => {
    if (selMethod !== 'oauth') return
    api.getOAuthCredentials(pid).then(json => {
      if (json) {
        try {
          const c = JSON.parse(json)
          setOauthCreds({ clientID: c.clientID || '', clientSecret: c.clientSecret || '' })
          setOauthCredsNeeded(false)
        } catch {}
      }
      setOauthCredsLoaded(true)
    })
  }, [pid, selMethod])

  // Subscribe to OAuth flow events; unsubscribe on unmount
  useEffect(() => {
    const offProgress = onConnectionProgress((data) => {
      if (data && data.platform === pid) {
        setFlowSteps(prev => [...prev, { message: data.message, kind: data.kind }])
      }
    })
    const offDone = onConnectionDone(async (data) => {
      if (!data || data.platform !== pid) return
      if (data.cancelled) { setFlowSteps([]); setFlowRunning(false); return }
      if (data.success) {
        setFlowRunning(false)
        await onRefresh()
        setTimeout(onClose, 1500)
      } else {
        setFlowSteps(prev => [...prev, { message: data.error || 'Flow failed', kind: 'error' }])
        setFlowRunning(false)
      }
    })
    return () => { offProgress(); offDone() }
  }, [pid, onRefresh, onClose])

  // Scroll to bottom when new steps arrive
  useEffect(() => {
    if (stepsEndRef.current) stepsEndRef.current.scrollIntoView({ behavior: 'smooth' })
  }, [flowSteps])

  const test = useCallback(async () => {
    if (!conn) return
    setTesting(true); setTestMsg(null)
    try {
      const result = await api.testConnection(conn.id)
      setTestMsg(result === 'ok' ? 'ok' : (result || 'error'))
    }
    finally { setTesting(false) }
  }, [conn])

  const disconnect = useCallback(async () => {
    if (!conn) return
    setRemoving(true)
    try {
      const r = await api.removeConnection(conn.id)
      if (r && r.startsWith('error:')) { setSaveErr(r.replace('error:', '').trim()); setRemoving(false); return }
      // Small delay to let SQLite commit the delete before reloading
      await new Promise(r => setTimeout(r, 200))
      await onDisconnect()
    } finally { setRemoving(false); setConfirmDisconnect(false) }
  }, [conn, onDisconnect])

  const connect = useCallback(async () => {
    setSaving(true); setSaveErr(null); setSaveOk(false)
    try {
      const r = await api.saveConnectionDirect(pid, selMethod, fields)
      if (r && r.startsWith('ok:')) {
        setSaveOk(true)
        await onRefresh()
        setTimeout(onClose, 1500)
      } else { setSaveErr(r ? r.replace('error:', '').trim() : 'Unknown error') }
    } finally { setSaving(false) }
  }, [pid, selMethod, fields, onRefresh, onClose])

  const startOAuthFlow = useCallback(async () => {
    setFlowSteps([])
    setFlowRunning(true)
    setSaveErr(null)
    const { pairs, secretJSON } = oauthFieldArgs(oauthFields, oauthValues)
    const r = await api.connectPlatformOAuth(pid, pairs, secretJSON)
    if (r && r.startsWith('error:')) {
      setFlowSteps([{ message: r.replace('error:', '').trim(), kind: 'error' }])
      setFlowRunning(false)
    }
  }, [pid, oauthFields, oauthValues])

  const dotColor = (kind) => {
    if (kind === 'success') return 'var(--green-neon)'
    if (kind === 'error')   return 'var(--red)'
    return 'var(--text-muted)'
  }
  const textColor = (kind) => {
    if (kind === 'success') return 'var(--green-neon)'
    if (kind === 'error')   return 'var(--red)'
    return 'var(--text-muted)'
  }

  return (
    <>
    <div
      onClick={e => e.target === e.currentTarget && onClose()}
      style={{ position: 'fixed', inset: 0, background: 'var(--overlay)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}
    >
      <div style={{ width: '100%', maxWidth: 440, background: 'var(--elevated)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius-xl)', boxShadow: 'var(--shadow-glow)', overflow: 'hidden' }}>

        {/* Header */}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '16px 20px 12px', borderBottom: '1px solid var(--border)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={{ fontSize: 26 }}>{emoji}</span>
            <div>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 13, fontWeight: 700, color: 'var(--text)' }}>{name}</div>
              <div style={{ display: 'flex', alignItems: 'center', gap: 5, marginTop: 2 }}>
                <span style={{ width: 6, height: 6, borderRadius: '50%', background: conn ? 'var(--green-neon)' : 'var(--text-muted)', boxShadow: conn ? '0 0 5px var(--green-neon)' : 'none' }} />
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: conn ? 'var(--green-neon)' : 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>
                  {conn ? 'Connected' : 'Not Connected'}
                </span>
              </div>
            </div>
          </div>
          <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 4, display: 'flex' }}>
            <X size={16} />
          </button>
        </div>

        {/* Body */}
        <div style={{ padding: '16px 20px 20px', display: 'flex', flexDirection: 'column', gap: 14 }}>
          {conn ? (
            /* ── Connected state ── */
            <>
              <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '10px 14px', display: 'flex', flexDirection: 'column', gap: 8 }}>
                {[['Account', conn.account], ['Method', conn.method], ['Status', conn.status], ['Last Tested', fmtDate(conn.lastTested)]].map(([lbl, val]) => (
                  <div key={lbl} style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>{lbl}</span>
                    <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: lbl === 'Status' ? (conn.status === 'active' ? 'var(--green-neon)' : 'var(--red)') : 'var(--text-secondary)' }}>{val}</span>
                  </div>
                ))}
              </div>
              {testMsg && (
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, padding: '7px 12px', borderRadius: 'var(--radius)', background: testMsg === 'ok' ? 'rgba(74,222,128,.08)' : 'rgba(239,68,68,.08)', border: `1px solid ${testMsg === 'ok' ? 'rgba(74,222,128,.25)' : 'rgba(239,68,68,.25)'}`, color: testMsg === 'ok' ? 'var(--green-neon)' : 'var(--red)' }}>
                  {testMsg === 'ok' ? '✓ Connection OK' : (
                    <div>
                      <div>✗ {testMsg.replace('error: ', '')}</div>
                      {conn.method === 'oauth' && (
                        <div style={{ marginTop: 8, fontSize: 10, color: 'var(--text-muted)' }}>
                          Your access token has expired. Click <strong style={{ color: 'var(--cyan)' }}>Reconnect</strong> below to re-authorize.
                        </div>
                      )}
                    </div>
                  )}
                </div>
              )}
              {saveErr && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, padding: '7px 12px', borderRadius: 'var(--radius)', background: 'rgba(239,68,68,.08)', border: '1px solid rgba(239,68,68,.25)', color: 'var(--red)' }}>{saveErr}</div>}
              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                <button className="btn btn-secondary btn-sm" onClick={test} disabled={testing || flowRunning} style={{ gap: 5 }}>
                  {testing ? <Loader size={11} style={{ animation: 'spin .7s linear infinite' }} /> : <CheckCircle size={11} />}
                  {testing ? 'Testing…' : 'Test'}
                </button>
                {conn.method === 'oauth' && testMsg && testMsg !== 'ok' && (
                  <button className="btn btn-primary btn-sm" onClick={startOAuthFlow} disabled={flowRunning} style={{ gap: 5 }}>
                    {flowRunning ? <Loader size={11} style={{ animation: 'spin .7s linear infinite' }} /> : <RefreshCw size={11} />}
                    {flowRunning ? 'Reconnecting…' : 'Reconnect'}
                  </button>
                )}
                {!confirmDisconnect ? (
                  <button className="btn btn-danger btn-sm" onClick={() => setConfirmDisconnect(true)} disabled={removing} style={{ gap: 5 }}>
                    <Trash2 size={11} /> Disconnect
                  </button>
                ) : (
                  <>
                    <button className="btn btn-danger btn-sm" onClick={disconnect} disabled={removing} style={{ gap: 5 }}>
                      {removing ? <Loader size={11} style={{ animation: 'spin .7s linear infinite' }} /> : <Trash2 size={11} />}
                      {removing ? 'Removing…' : 'Yes, disconnect'}
                    </button>
                    <button className="btn btn-ghost btn-sm" onClick={() => setConfirmDisconnect(false)}>Cancel</button>
                  </>
                )}
                {!confirmDisconnect && <button className="btn btn-ghost btn-sm" onClick={onClose}>Close</button>}
              </div>
              {flowSteps.length > 0 && (
                <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '8px 10px', maxHeight: 120, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 5 }}>
                  {flowSteps.map((step, i) => (
                    <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                      <span style={{ width: 5, height: 5, borderRadius: '50%', background: dotColor(step.kind), flexShrink: 0 }} />
                      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: textColor(step.kind) }}>{step.message}</span>
                    </div>
                  ))}
                </div>
              )}
            </>
          ) : (
            /* ── Not connected state ── */
            <>
              {/* Method selector */}
              {methods.length > 1 && (
                <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                  {methods.map(m => (
                    <button key={m} onClick={() => { setSelMethod(m); setFields({}); setOauthValues({}); setSaveErr(null); setSaveOk(false); setFlowSteps([]); setFlowRunning(false); setShowHelp(false) }}
                      style={{ fontFamily: 'var(--font-mono)', fontSize: 10, padding: '3px 10px', borderRadius: 4, background: selMethod === m ? 'var(--cyan-dim)' : 'var(--surface)', border: selMethod === m ? '1px solid var(--cyan)' : '1px solid var(--border)', color: selMethod === m ? 'var(--bg)' : 'var(--text-muted)', cursor: 'pointer' }}>
                      {m}
                    </button>
                  ))}
                </div>
              )}

              {/* OAuth flow */}
              {isOAuth ? (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                  {/* OAuth app credentials — always shown so user can update them */}
                  {needsClient && (oauthCredsNeeded || flowSteps.length === 0) && !flowRunning && (
                    <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 8 }}>
                      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1.5 }}>
                          OAuth App Credentials
                        </div>
                        {guide && (
                          <button onClick={() => setShowHelp(true)} style={{ background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 3, color: 'var(--cyan)', fontFamily: 'var(--font-mono)', fontSize: 10, padding: 0 }}>
                            <HelpCircle size={11} /> How to get these
                          </button>
                        )}
                      </div>
                      <input
                        type="text"
                        placeholder="Client ID"
                        value={oauthCreds.clientID}
                        onChange={e => setOauthCreds(p => ({ ...p, clientID: e.target.value }))}
                        style={{ fontFamily: 'var(--font-mono)', fontSize: 11, background: 'var(--elevated)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '6px 10px', outline: 'none' }}
                      />
                      <input
                        type="password"
                        placeholder="Client Secret (optional — leave blank for public/native app registrations)"
                        value={oauthCreds.clientSecret}
                        onChange={e => setOauthCreds(p => ({ ...p, clientSecret: e.target.value }))}
                        style={{ fontFamily: 'var(--font-mono)', fontSize: 11, background: 'var(--elevated)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '6px 10px', outline: 'none' }}
                      />
                      <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                        <button
                          className="btn btn-primary btn-sm"
                          disabled={!oauthCreds.clientID}
                          onClick={async () => {
                            const r = await api.setOAuthCredentials(pid, oauthCreds.clientID, oauthCreds.clientSecret)
                            if (r === 'ok') { setOauthCredsSaved(true); setOauthCredsNeeded(false); setTimeout(() => setOauthCredsSaved(false), 2000) }
                            else setSaveErr(r.replace('error:', '').trim())
                          }}
                          style={{ gap: 5 }}
                        >
                          <CheckCircle size={11} /> Save credentials
                        </button>
                        {oauthCredsSaved && <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--green-neon)' }}>Saved ✓</span>}
                      </div>
                    </div>
                  )}

                  {!flowRunning && (
                    <OAuthFields title={selMethod === 'oauth1' ? 'OAuth 1.0a app' : 'Account details'}
                      fields={oauthFields} values={oauthValues} onChange={setOauthValues} />
                  )}

                  {flowSteps.length === 0 && !flowRunning && (
                    <>
                      <button
                        className="btn btn-secondary btn-sm"
                        disabled={!canStart}
                        onClick={startOAuthFlow}
                        style={{ gap: 5, alignSelf: 'flex-start' }}
                        title={canStart ? '' : (needsClient && !oauthCreds.clientID ? 'Save your OAuth credentials first' : 'Fill in the required fields first')}
                      >
                        <CheckCircle size={11} /> Connect with {name}
                      </button>
                      {!canStart && (
                        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>
                          {needsClient && !oauthCreds.clientID ? 'Save your OAuth credentials above first' : 'Fill in the required fields above first'}
                        </span>
                      )}
                    </>
                  )}
                  {flowSteps.length > 0 && (
                    <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '8px 10px', maxHeight: 160, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 5 }}>
                      {flowSteps.map((step, i) => (
                        <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                          <span style={{ width: 6, height: 6, borderRadius: '50%', background: dotColor(step.kind), flexShrink: 0, boxShadow: step.kind === 'success' ? '0 0 4px var(--green-neon)' : 'none' }} />
                          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: textColor(step.kind) }}>{step.message}</span>
                        </div>
                      ))}
                      <div ref={stepsEndRef} />
                    </div>
                  )}
                  {flowRunning && (
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                      <Loader size={11} style={{ animation: 'spin .7s linear infinite', color: 'var(--text-muted)' }} />
                      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>Running…</span>
                      <button className="btn btn-ghost btn-sm" onClick={() => api.cancelPlatformOAuth(pid)} style={{ gap: 5 }}>
                        <X size={11} /> Cancel
                      </button>
                    </div>
                  )}
                  {!flowRunning && flowSteps.some(s => s.kind === 'error') && (
                    <button className="btn btn-primary btn-sm" onClick={startOAuthFlow} disabled={!canStart} style={{ gap: 5, alignSelf: 'flex-start' }}>
                      <RefreshCw size={11} /> Retry
                    </button>
                  )}
                </div>
              ) : selMethod === 'connstring' ? (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                    <label style={{ fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1.5 }}>Connection String <span style={{ color: 'var(--red)' }}>*</span></label>
                    {guide && (
                      <button onClick={() => setShowHelp(true)} style={{ background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 3, color: 'var(--cyan)', fontFamily: 'var(--font-mono)', fontSize: 10, padding: 0 }}>
                        <HelpCircle size={11} /> How to get this
                      </button>
                    )}
                  </div>
                  <textarea rows={3} value={fields['connection_string'] || ''} onChange={e => setFields({ connection_string: e.target.value })}
                    placeholder="postgresql://user:pass@host:5432/dbname"
                    style={{ width: '100%', fontFamily: 'var(--font-mono)', fontSize: 11, background: 'var(--surface)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '8px 10px', resize: 'vertical', outline: 'none', boxSizing: 'border-box' }} />
                </div>
              ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                  {guide && (
                    <button onClick={() => setShowHelp(true)} style={{ background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 4, color: 'var(--cyan)', fontFamily: 'var(--font-mono)', fontSize: 10, padding: '4px 0', alignSelf: 'flex-start' }}>
                      <HelpCircle size={12} /> How to get your credentials
                    </button>
                  )}
                  {mFields.length === 0 ? (
                    <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No configuration required.</div>
                  ) : mFields.map(f => (
                    <div key={f.Key || f.key}>
                      <label style={{ display: 'block', fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1.5, marginBottom: 4 }}>
                        {f.Label || f.label}{(f.Required || f.required) && <span style={{ color: 'var(--red)' }}> *</span>}
                      </label>
                      <input type={(f.Secret || f.secret) ? 'password' : 'text'}
                        value={fields[f.Key || f.key] || ''}
                        onChange={e => setFields(prev => ({ ...prev, [f.Key || f.key]: e.target.value }))}
                        autoComplete={(f.Secret || f.secret) ? 'new-password' : 'off'}
                        style={{ width: '100%', fontFamily: 'var(--font-mono)', fontSize: 11, background: 'var(--surface)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '7px 10px', outline: 'none', boxSizing: 'border-box' }} />
                      {(f.HelpText || f.helpText) && <div style={{ fontFamily: 'var(--font-body)', fontSize: 10, color: 'var(--text-muted)', marginTop: 3 }}>{f.HelpText || f.helpText}</div>}
                    </div>
                  ))}
                </div>
              )}

              {saveErr && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, padding: '7px 12px', borderRadius: 'var(--radius)', background: 'rgba(239,68,68,.08)', border: '1px solid rgba(239,68,68,.25)', color: 'var(--red)' }}>{saveErr}</div>}
              {saveOk  && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, padding: '7px 12px', borderRadius: 'var(--radius)', background: 'rgba(74,222,128,.08)', border: '1px solid rgba(74,222,128,.25)', color: 'var(--green-neon)' }}>Connected!</div>}

              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                {!isOAuth && (mFields.length > 0 || selMethod === 'connstring') && (
                  <button className="btn btn-primary btn-sm" onClick={connect} disabled={saving || saveOk} style={{ gap: 5 }}>
                    {saving ? <Loader size={11} style={{ animation: 'spin .7s linear infinite' }} /> : <CheckCircle size={11} />}
                    {saving ? 'Connecting…' : 'Connect'}
                  </button>
                )}
                <button className="btn btn-secondary btn-sm" onClick={onRefresh} style={{ gap: 5 }}><RefreshCw size={11} /> Refresh</button>
                <button className="btn btn-ghost btn-sm" onClick={onClose}>Close</button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>

    {showHelp && guide && (
      <HelpModal
        guide={guide}
        platformName={name}
        emoji={emoji}
        methodLabel={selMethod}
        onClose={() => setShowHelp(false)}
      />
    )}
    </>
  )
}

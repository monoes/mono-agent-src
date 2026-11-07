import { useState, useEffect, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw, Bot, MessageSquare, Download, Copy, Loader2, ArrowUpCircle } from 'lucide-react'
import { cachedAgentScan, invalidateAgentScan, installRecipe, recipeCommand } from '../lib/agentRuntimes.js'
import { runtimeLabel } from '../lib/runtimeLabels.js'
import { installRuntime, runHealth } from '../lib/health.js'
import { api } from '../services/api.js'
import { confirm } from '../components/ConfirmDialog.jsx'
import MonomindInitPrompt from '../components/MonomindInitPrompt.jsx'
import AgentRoster from '../components/agents/AgentRoster.jsx'
import OpenRunBoss from '../components/agents/OpenRunBoss.jsx'
import { useSendToCoder } from '../components/bubbles/CoderBubbleContext.jsx'

function statusColor(installed) {
  return installed ? 'var(--green-neon)' : 'var(--text-muted)'
}

// installCommand is what installing really runs, as monoagentcli decides
// it (installRecipe): the structured recipe when monomind sent one (agent
// scan protocol rev 9), else the hint when it is exactly an npm or
// `curl … | bash` install. A free-text hint, which can differ, is never it.
export function installCommand(agent) {
  return recipeCommand(installRecipe(agent), agent)
}

const tileButton = { gap: 4, marginTop: 2, fontSize: 10 }
const tileNote = { fontFamily: 'var(--font-mono)', fontSize: 8.5, textAlign: 'center', lineHeight: 1.4, maxWidth: '100%' }

function RuntimeTile({ agent, onChat, onInstall, job }) {
  const { t } = useTranslation()
  const [hov, setHov] = useState(false)
  const kind = installRecipe(agent).kind
  const running = job?.running
  return (
    <div
      data-runtime={agent.id}
      onMouseEnter={() => setHov(true)}
      onMouseLeave={() => setHov(false)}
      style={{
        background: agent.installed ? 'linear-gradient(145deg,var(--elevated),var(--surface))' : 'var(--surface)',
        border: agent.installed ? '1px solid var(--border-active)' : hov ? '1px solid var(--border-bright)' : '1px solid var(--border)',
        borderRadius: 'var(--radius-lg)',
        padding: '16px 12px 12px',
        display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 8,
        transition: 'all var(--transition)',
        boxShadow: agent.installed ? 'var(--shadow-glow)' : 'none',
        minWidth: 0,
      }}
    >
      <Bot size={22} style={{ color: agent.installed ? 'var(--cyan)' : 'var(--text-muted)', opacity: agent.installed ? 1 : 0.5 }} />
      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 600, color: agent.installed ? 'var(--text)' : 'var(--text-secondary)', textAlign: 'center' }}>
        {runtimeLabel(agent.id)}
      </span>
      <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
        <span style={{ width: 6, height: 6, borderRadius: '50%', background: statusColor(agent.installed), boxShadow: agent.installed ? '0 0 5px var(--green-neon)' : 'none', flexShrink: 0 }} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)' }}>
          {agent.installed ? (agent.version || t('agents.installed')) : t('agents.notInstalled')}
        </span>
      </div>
      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', justifyContent: 'center' }}>
        {agent.installed && (
          <button className="btn btn-sm" onClick={() => onChat(agent.id)} style={tileButton}>
            <MessageSquare size={11} /> {t('agents.chat')}
          </button>
        )}
        {running ? (
          <button className="btn btn-sm" disabled style={tileButton}>
            <Loader2 size={11} className="spin" /> {agent.installed ? t('agents.updating') : t('agents.installing')}
          </button>
        ) : kind === 'manual' ? (
          !agent.installed && (
            <button className="btn btn-sm btn-ghost" onClick={() => onInstall(agent, 'copy')} title={agent.install_hint} style={tileButton}>
              <Copy size={11} /> {t('agents.copySteps')}
            </button>
          )
        ) : agent.installed ? (
          <button className="btn btn-sm btn-ghost" onClick={() => onInstall(agent, 'update')} title={agent.install_hint} style={tileButton}>
            <ArrowUpCircle size={11} /> {t('agents.update')}
          </button>
        ) : (
          <button className="btn btn-sm btn-primary" onClick={() => onInstall(agent, 'install')} title={agent.install_hint} style={tileButton}>
            <Download size={11} /> {t('agents.install')}
          </button>
        )}
      </div>
      {/* The sign-in step stays visible after an install: it's the next thing to do. */}
      {agent.installed && agent.login_hint && !running && (
        <div style={{ ...tileNote, color: 'var(--text-muted)' }}>
          {t('agents.signIn', { hint: agent.login_hint })}
        </div>
      )}
      {!agent.installed && kind === 'manual' && agent.install_hint && !job && (
        <div style={{ ...tileNote, color: 'var(--text-muted)', maxWidth: 140 }}>
          {agent.install_hint}
        </div>
      )}
      {/* Always rendered, so screen readers announce what appears in it. An
          error is shown in full: it is what the person needs to act on. */}
      <div role="status" aria-live="polite" data-testid={`install-result-${agent.id}`} style={{ ...tileNote, width: '100%' }}>
        {job && (job.line || job.error || job.done) && (
          job.error ? (
            <div style={{ color: 'var(--red)', whiteSpace: 'pre-wrap', wordBreak: 'break-word', textAlign: 'left', maxHeight: 120, overflowY: 'auto' }}>
              {t('agents.installFailed')}: {job.error}
            </div>
          ) : (
            <div style={{ color: job.done ? 'var(--green-neon)' : 'var(--text-muted)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={job.line}>
              {job.done ? (job.message || t('agents.done')) : job.line}
            </div>
          )
        )}
      </div>
    </div>
  )
}

// `agent scan` is a genuinely slow operation (~6-7s: it spawns monomind,
// which itself spawns a handshake + a parallel probe of every known agent
// CLI binary). Within one running app session, keeping Agents mounted
// (App.jsx's keep-alive navigation) means this only runs once. But every
// fresh app launch is a new session with nothing cached — so we also
// persist the last scan result to localStorage and paint it immediately on
// mount, then silently refresh in the background. The user sees last-known
// state instantly instead of a multi-second spinner on every single app
// start, and the display self-corrects within a few seconds if anything
// changed (a runtime got installed/removed, monomind's version changed).
const SCAN_CACHE_KEY = 'monoagent:agentScanCache:v1'

function readScanCache() {
  try {
    const raw = localStorage.getItem(SCAN_CACHE_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

function writeScanCache(res) {
  try {
    localStorage.setItem(SCAN_CACHE_KEY, JSON.stringify({ res, cachedAt: Date.now() }))
  } catch { /* localStorage unavailable/full — cache is best-effort */ }
}

export default function Agents() {
  // Chat on a runtime opens a new chat bubble with that runtime picked.
  const { openNewChat } = useSendToCoder()
  const onOpenChat = useCallback(runtimeId => openNewChat?.({ setup: { runtime: runtimeId } }), [openNewChat])
  const { t } = useTranslation()
  const cached = readScanCache()
  const [agents, setAgents] = useState(() => (cached && !cached.res?.error) ? (cached.res.agents || []) : [])
  const [scanError, setScanError] = useState(() => cached?.res?.error || null)
  // Only show the blocking spinner when there is truly nothing to paint yet
  // (first-ever run on this machine, or a cleared cache). Otherwise show
  // stale-but-instant data while refreshing quietly underneath it.
  const [loading, setLoading] = useState(() => !cached)

  const loadAgents = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      // Shared TTL-cached scan — a new chat's runtime scan right after this
      // page scanned reconciles against the same result, no second 7s scan.
      const res = await cachedAgentScan()
      if (!res || res.error) {
        setScanError(res?.error || t('agents.unreachable'))
        setAgents([])
      } else {
        setScanError(null)
        setAgents(res.agents || [])
      }
      writeScanCache(res)
    } finally {
      if (!silent) setLoading(false)
    }
  }, [t])

  // First mount: if we already painted from cache, refresh silently (no
  // spinner) — otherwise this is the one real blocking load.
  useEffect(() => { loadAgents(!!cached) }, [loadAgents])

  // Independent of the runtime scan above: monomind can be installed
  // globally (scanError below only fires when the binary itself is
  // missing/outdated) while this profile's own folder has never been
  // initialized — a separate, per-profile check.
  const [notInitialized, setNotInitialized] = useState(false)
  useEffect(() => {
    api.isMonomindInitialized().then(v => setNotInitialized(!v))
  }, [])

  // Install / update / copy-steps per runtime — all through monoagentcli
  // (`agent install`), shown inline on the tile.
  const [jobs, setJobs] = useState({})
  const setJob = (id, patch) => setJobs(j => ({ ...j, [id]: patch === null ? undefined : { ...(j[id] || {}), ...patch } }))
  const onInstall = useCallback(async (agent, action) => {
    if (action === 'copy') {
      try { await navigator.clipboard.writeText(agent.install_hint || '') } catch { /* clipboard may be blocked */ }
      setJob(agent.id, { line: t('agents.copied'), done: false, error: null })
      return
    }
    const recipe = installRecipe(agent)
    const script = recipe.kind === 'script'
    // A script install approves exactly the URL shown here: the CLI runs it
    // only if its fresh scan names the same one.
    const approveURL = script ? recipe.url : ''
    const update = action === 'update'
    const ok = await confirm(
      <span>
        {update ? t('agents.confirmUpdate', { id: agent.id }) : t('agents.confirmInstall', { id: agent.id })}{' '}
        {script ? t('agents.runsScript') : t('agents.runs')}
        <code style={{ display: 'block', marginTop: 8, padding: '6px 8px', background: 'rgba(0,0,0,.35)', borderRadius: 4, wordBreak: 'break-all' }}>
          {recipeCommand(recipe, agent)}
        </code>
      </span>,
      {
        title: update ? t('agents.updateTitle', { id: agent.id }) : t('agents.installTitle', { id: agent.id }),
        confirmLabel: update ? t('agents.update') : t('agents.install'), cancelLabel: t('agents.cancel'), danger: false,
      },
    )
    if (!ok) return
    setJob(agent.id, { running: true, line: t('agents.starting'), error: null, done: false })
    const res = await installRuntime(agent.id, update, line => setJob(agent.id, { line }), approveURL)
    // busy: the same runtime is being installed from System health.
    const error = res.ok ? null : res.busy ? t('agents.alreadyInstalling', { id: agent.id }) : res.cancelled ? t('agents.cancelled') : res.message
    setJob(agent.id, { running: false, done: res.ok, error, message: res.message })
    if (res.busy) return
    invalidateAgentScan()
    loadAgents(true)
    runHealth()
  }, [loadAgents, t])

  const installedCount = agents.filter(a => a.installed).length

  return (
    <div style={{ display: 'flex', height: '100%', overflow: 'hidden' }}>
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <div className="page-header">
          <div className="page-header-left">
            <div className="page-title">{t('agents.title')}</div>
            <div className="page-subtitle">
              {loading ? t('agents.loading') : scanError ? t('agents.monomindUnavailable') : t('agents.installedCount', { installed: installedCount, total: agents.length })}
            </div>
          </div>
          <div className="page-header-right" style={{ display: 'flex', gap: 6 }}>
            <button className="btn btn-ghost btn-sm" onClick={() => { invalidateAgentScan(); loadAgents() }} style={{ gap: 5 }}><RefreshCw size={12} /> {t('agents.refresh')}</button>
          </div>
        </div>

        <div className="page-body" style={{ flex: 1, overflow: 'auto' }}>
          {/* A live open-ended run's boss is shown whatever state the rest of the page is in: a run started from a chat must not
              need agents to be set up, installed or scanned. It renders nothing when no run's boss is open. */}
          <OpenRunBoss />
          {loading ? (
            <div className="empty-state"><div className="spinner" /></div>
          ) : scanError ? (
            <div className="empty-state">
              <div className="empty-state-icon"><Bot size={36} /></div>
              <div className="empty-state-title">
                {/not found/i.test(scanError) ? t('agents.monomindNotInstalled') : t('agents.monomindOutdated')}
              </div>
              <div className="empty-state-desc" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                <span>
                  {/* Fallback copy aligned with internal/monomind/find.go's
                      ErrNotFound / update prescriptions; the backend's own
                      error text (which embeds the exact command) is rendered
                      verbatim below. */}
                  {t('agents.engineNote')} {/not found/i.test(scanError)
                    ? <>{t('agents.installWith')} <code>npm install -g @monoes/monomindcli</code></>
                    : <>{t('agents.updateWith')} <code>npm install -g @monoes/monomindcli@latest</code></>}
                </span>
                <code style={{ fontSize: 10, color: 'var(--text-muted)', wordBreak: 'break-word', textAlign: 'left' }}>{scanError}</code>
              </div>
            </div>
          ) : notInitialized ? (
            <MonomindInitPrompt onInitialized={() => { setNotInitialized(false); loadAgents() }} />
          ) : agents.length === 0 ? (
            <div className="empty-state">
              <div className="empty-state-icon"><Bot size={36} /></div>
              <div className="empty-state-title">{t('agents.noRuntimesTitle')}</div>
              <div className="empty-state-desc">{t('agents.noRuntimesDesc')}</div>
            </div>
          ) : (
            <>
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(140px, 1fr))', gap: 10 }}>
                {agents.map(a => (
                  <RuntimeTile key={a.id} agent={a} onChat={onOpenChat} onInstall={onInstall} job={jobs[a.id]} />
                ))}
              </div>
              <AgentRoster />
            </>
          )}
        </div>
      </div>
    </div>
  )
}

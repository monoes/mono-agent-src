// Step 6, Your agents: scans the AI coding tools on this machine
// (ScanAgentRuntimes, which needs only the monomind binary, no profile init),
// shows a tile per runtime, installs npm runtimes through `agent install`, and
// checks one runtime with one short test message (StartAgentValidation with
// one runtime and one model). Continue is enabled once `agent roster` holds a
// model in the ready state: the same fact the Go side derives this step's
// status from, so the step writes no mark of its own.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import * as GoApp from '../../wailsjs/go/main/App'
import { onAgentValidate, onAgentValidateClosed } from '../../services/api.js'
import { callStep } from '../callStep.js'
import { installRecipe, invalidateAgentScan, isMonomindNotFound } from '../../lib/agentRuntimes.js'
import { installRuntime } from '../../lib/health.js'
import AgentTile, { rosterReady, runtimeName } from './AgentTile.jsx'
import './Agents.css'

// internal/account/errors.go LoginRequiredMessage starts with this; the
// install helper passes the refusal on as plain text.
const LOGIN_LINE = 'Log in to monoes.me first'
const MAX_LINES = 50
const works = (status) => status === 'ok' || status === 'ok_unexpected'
const runnable = (a) => a.installed && a.execution_supported !== false

const Pin = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M12 21s-6-5.3-6-10a6 6 0 0 1 12 0c0 4.7-6 10-6 10z" /><circle cx="12" cy="11" r="2.2" />
  </svg>
)

// The pick shown when nothing is installed: Claude Code when the scan lists
// it, else the first runtime the CLI can install by itself, else the first.
function recommended(agents) {
  const pick = agents.find(a => a.id === 'claude') || agents.find(a => installRecipe(a).kind === 'npm') || agents[0]
  return pick ? runtimeName(pick.id) : ''
}

export default function Agents({ reload, goTo, setCanContinue }) {
  const { t } = useTranslation()
  const [scan, setScan] = useState({ scanning: true, agents: null, failed: null })
  const [roster, setRoster] = useState([])
  const [models, setModels] = useState({})
  const [picked, setPicked] = useState({})
  const [test, setTestState] = useState(null)
  const [installs, setInstalls] = useState({})
  const live = useRef(true)
  const testRef = useRef(null)
  const resultRef = useRef(null)
  const modelsAsked = useRef(new Set())
  const reloadRef = useRef(reload)
  reloadRef.current = reload

  const setTest = (v) => { testRef.current = v; setTestState(v) }
  const setInstall = (id, patch) => setInstalls(m => ({ ...m, [id]: patch && { ...(m[id] || {}), ...patch } }))

  const loadRoster = useCallback(async () => {
    const r = await callStep(() => GoApp.AgentRoster())
    if (!live.current || !r.ok) return null
    const runtimes = Array.isArray(r.value?.runtimes) ? r.value.runtimes : []
    setRoster(runtimes)
    return runtimes
  }, [])

  const loadModels = useCallback(async (agent) => {
    if (modelsAsked.current.has(agent.id)) return
    modelsAsked.current.add(agent.id)
    setModels(m => ({ ...m, [agent.id]: { loading: true, list: [], error: '' } }))
    const r = await callStep(() => GoApp.GetAgentRuntimeModels(agent.id, agent.binary || ''))
    if (!live.current) return
    if (!r.ok) {
      modelsAsked.current.delete(agent.id) // Check again asks once more
      setModels(m => ({ ...m, [agent.id]: { loading: false, list: [], error: r.message } }))
      return
    }
    const list = (Array.isArray(r.value) ? r.value : []).filter(x => x && x.id && !x.alias_of)
    setModels(m => ({ ...m, [agent.id]: { loading: false, list, error: '' } }))
    setPicked(p => (p[agent.id] ? p : { ...p, [agent.id]: list[0]?.id || '' }))
  }, [])

  const load = useCallback(async () => {
    setScan(s => ({ ...s, scanning: true }))
    const [r] = await Promise.all([callStep(() => GoApp.ScanAgentRuntimes()), loadRoster()])
    if (!live.current) return
    if (!r.ok) {
      setScan({ scanning: false, agents: null, failed: { message: r.message, loginRequired: r.loginRequired, missing: isMonomindNotFound(r.message) } })
      return
    }
    const agents = Array.isArray(r.value?.agents) ? r.value.agents : []
    setScan({ scanning: false, agents, failed: null })
    agents.filter(runnable).forEach(loadModels)
  }, [loadRoster, loadModels])

  const checkAgain = () => { invalidateAgentScan(); load() }

  useEffect(() => {
    live.current = true
    load()
    // Only the run this step started counts; a validation started elsewhere
    // (Settings, the automatic re-check) is left to the roster read.
    const offLine = onAgentValidate((line) => {
      const cur = testRef.current
      if (!cur || cur.phase !== 'running' || line?.type !== 'validate.result') return
      const r = line.result || {}
      if (r.runtime === cur.runtime && r.model === cur.model) resultRef.current = r
    })
    const offClosed = onAgentValidateClosed(async (res) => {
      const cur = testRef.current
      if (!cur || cur.phase !== 'running') return
      const r = resultRef.current
      if (r && works(r.status)) setTest({ ...cur, phase: 'passed' })
      else setTest({ ...cur, phase: 'failed', message: r ? (r.detail || r.status || '') : (res?.error || '') })
      const runtimes = await loadRoster()
      if (live.current && runtimes && runtimes.some(rosterReady)) reloadRef.current?.()
    })
    return () => { live.current = false; offLine(); offClosed() }
  }, [load, loadRoster])

  const ready = roster.some(rosterReady)
  useEffect(() => { setCanContinue(ready) }, [ready, setCanContinue])

  const onTest = async (agent) => {
    const model = picked[agent.id]
    if (!model || testRef.current?.phase === 'running') return
    resultRef.current = null
    const run = { runtime: agent.id, model, phase: 'running' }
    setTest(run)
    const r = await callStep(() => GoApp.StartAgentValidation([agent.id], [model], false))
    if (!r.ok && live.current && testRef.current === run) {
      setTest({ ...run, phase: 'failed', message: r.message, loginRequired: r.loginRequired })
    }
  }

  const onInstall = async (agent) => {
    setInstall(agent.id, { running: true, lines: [], error: '', busy: false, loginRequired: false })
    const res = await installRuntime(agent.id, false, (line) => {
      if (live.current) setInstalls(m => ({ ...m, [agent.id]: { ...m[agent.id], lines: [...(m[agent.id]?.lines || []), line].slice(-MAX_LINES) } }))
    })
    if (!live.current) return
    if (res.ok) {
      setInstall(agent.id, null)
      invalidateAgentScan()
      load()
      return
    }
    const message = res.message || ''
    setInstall(agent.id, { running: false, error: message || 'failed', busy: !!res.busy, loginRequired: message.startsWith(LOGIN_LINE) })
  }

  const agents = scan.agents
  const ordered = agents ? [...agents.filter(a => a.installed), ...agents.filter(a => !a.installed)] : []
  const testBusy = test?.phase === 'running'
  let body
  if (scan.failed && scan.failed.missing) {
    body = (
      <div className="onb-card onb-ag-missing">
        <p>{t('onboarding.agents.monomindMissing')}</p>
        <button type="button" className="onb-btn" onClick={() => goTo('system')}>{t('onboarding.agents.backToSystem')}</button>
      </div>
    )
  } else if (scan.failed) {
    body = (
      <p className="onb-error" role="alert">
        {scan.failed.loginRequired ? t('onboarding.agents.signInScan') : t('onboarding.agents.scanFailed', { message: scan.failed.message })}
      </p>
    )
  } else if (!agents) {
    body = (
      <div className="onb-rows">
        <div className="onb-row busy" role="status">
          <span className="onb-dot"><span className="onb-spinner" /></span>
          <div className="onb-row-t"><b>{t('onboarding.agents.scanning')}</b><span>{t('onboarding.agents.scanningHint')}</span></div>
        </div>
      </div>
    )
  } else {
    body = (
      <>
        {!agents.some(a => a.installed) && (
          <div className="onb-ag-empty" role="status">
            <b>{t('onboarding.agents.emptyTitle')}</b>
            {recommended(agents) && <span>{t('onboarding.agents.recommended', { name: recommended(agents) })}</span>}
          </div>
        )}
        <div className="onb-grid2 onb-ag-tiles">
          {ordered.map(a => (
            <AgentTile key={a.id} agent={a} rr={roster.find(x => x.runtime === a.id)} models={models[a.id]}
              picked={picked[a.id]} onPick={(id, v) => setPicked(p => ({ ...p, [id]: v }))}
              test={test} testBusy={testBusy} onTest={onTest} install={installs[a.id]} onInstall={onInstall} />
          ))}
        </div>
      </>
    )
  }

  return (
    <>
      <p className="onb-lead">{t('onboarding.agents.lead')}</p>
      {!(scan.failed && scan.failed.missing) && (
        <div className="onb-ag-bar">
          <button type="button" className="onb-btn sm ghost" onClick={checkAgain} disabled={scan.scanning} aria-busy={scan.scanning || undefined}>
            {t('onboarding.agents.checkAgain')}
          </button>
        </div>
      )}
      {body}
      <p className="onb-pointer"><Pin /><span>{t('onboarding.agents.pointer')}</span></p>
    </>
  )
}

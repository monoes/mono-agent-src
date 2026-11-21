// Step 2, System check: four rows read from one `monoagentcli doctor --json`
// report (RunHealthCheck), runs before any profile exists. A failing row offers
// the fix its report names (RunHealthFix), shows the fix's streamed lines
// (health:fixProgress) and, when the fix is done, checks again: a row turns
// green only from a new report. All four green marks the step.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { RunHealthCheck, RunHealthFix } from '../../wailsjs/go/main/App'
import { subscribeEvent } from '../../services/api.js'
import { callStep } from '../callStep.js'
import './SystemCheck.css'

// The doctor mode the step asks for (app_health.go healthArgs).
export const HEALTH_MODE = 'background'

// Go refuses a second check of a mode while one runs ("a background check is
// already running"), and a second run of a fix ("… is already running"). That
// is the person coming back mid-check, never a failure: a check is asked
// again after RETRY_MS, and a fix is followed through the events it already
// sends under its id.
export const RETRY_MS = 1000
const ALREADY = /already (running|being installed)/

// The four rows, by doctor check id. The command line's check never says ok,
// only info (it reports the version); the other three pass only on ok.
const ROWS = [
  { id: 'core.cli', key: 'cli', passes: s => s === 'ok' || s === 'info', absent: () => true },
  { id: 'monomind.node', key: 'node', passes: s => s === 'ok', absent: r => r.summary === 'not found' },
  { id: 'monomind.binary', key: 'monomind', passes: s => s === 'ok', absent: r => r.status === 'fail' },
  { id: 'services.daemon', key: 'service', passes: s => s === 'ok', absent: r => r.fix?.id === 'services.daemon.start' },
]

const SvgIcon = ({ d }) => (
  <svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round"><path d={d} /></svg>
)
const CHECK = 'M5 12.5l4.5 4.5L19 7.5'
const CROSS = 'M7 7l10 10M17 7L7 17'
const DOT = 'M12 12h.01'

// What the fix button says, and what the row says while the fix runs.
function fixWords(t, id) {
  switch (id) {
    case 'monomind.install':
    case 'monomind.node.install':
      return [t('onboarding.system.fix.install'), t('onboarding.system.fix.installing')]
    case 'services.daemon.start':
      return [t('onboarding.system.fix.start'), t('onboarding.system.fix.starting')]
    case 'services.daemon.restart':
      return [t('onboarding.system.fix.restart'), t('onboarding.system.fix.restarting')]
    default:
      return [t('onboarding.system.fix.fix'), t('onboarding.system.fix.fixing')]
  }
}

function rowTitle(t, key) {
  switch (key) {
    case 'cli': return t('onboarding.system.rows.cli')
    case 'node': return t('onboarding.system.rows.node')
    case 'monomind': return t('onboarding.system.rows.monomind')
    default: return t('onboarding.system.rows.service')
  }
}
function rowNote(t, key) {
  if (key === 'monomind') return t('onboarding.system.rows.monomindNote')
  if (key === 'service') return t('onboarding.system.rows.serviceNote')
  return ''
}
function okWord(t, key) {
  if (key === 'monomind') return t('onboarding.system.status.installed')
  if (key === 'service') return t('onboarding.system.status.running')
  return t('onboarding.system.status.found')
}
function absentWord(t, key) {
  if (key === 'monomind') return t('onboarding.system.status.notInstalled')
  if (key === 'service') return t('onboarding.system.status.notRunning')
  return t('onboarding.system.status.notFound')
}

export default function SystemCheck({ mark, setCanContinue, setOnContinue }) {
  const { t } = useTranslation()
  const [results, setResults] = useState(null) // { [check id]: row } from the last report
  const [checking, setChecking] = useState(true)
  const [cliMissing, setCliMissing] = useState(false)
  const [notice, setNotice] = useState('')
  const [fixes, setFixes] = useState({}) // { [fix id]: { running, line, error } }
  const alive = useRef(true)
  const started = useRef(false)
  const busy = useRef(false)
  const again = useRef(false)
  const marked = useRef(false)
  const offs = useRef(new Set())

  const patchFix = (id, patch) => setFixes(s => ({ ...s, [id]: { ...(s[id] || {}), ...patch } }))

  const checkOnce = useCallback(async () => {
    setChecking(true)
    setNotice('')
    let raw
    let r
    for (;;) {
      raw = undefined
      r = await callStep(async () => (raw = await RunHealthCheck(HEALTH_MODE)))
      if (!alive.current) return
      if (r.ok || r.loginRequired || !ALREADY.test(r.message)) break
      await new Promise(res => setTimeout(res, RETRY_MS))
      if (!alive.current) return
    }
    if (r.ok) {
      const byId = {}
      for (const row of r.value?.results || []) byId[row.id] = row
      setResults(byId)
      setCliMissing(false)
    } else {
      let missing = false
      try { missing = JSON.parse(raw)?.cli_missing === true } catch { /* not JSON: an ordinary failure */ }
      setResults(null)
      setCliMissing(missing)
      setNotice(r.loginRequired ? t('onboarding.shell.signIn') : missing ? t('onboarding.system.cliMissing') : r.message)
    }
    setChecking(false)
  }, [t])

  // One check at a time (Go refuses a second one of the same mode); a check
  // asked for while one runs runs right after it.
  const check = useCallback(async () => {
    if (busy.current) { again.current = true; return }
    busy.current = true
    try {
      do {
        again.current = false
        await checkOnce()
      } while (again.current && alive.current)
    } finally {
      busy.current = false
    }
  }, [checkOnce])

  useEffect(() => {
    alive.current = true
    if (!started.current) { started.current = true; check() }
    const subs = offs.current
    return () => {
      alive.current = false
      for (const off of subs) off()
      subs.clear()
    }
  }, [check])

  const runFix = async (fix) => {
    if (fixes[fix.id]?.running) return
    patchFix(fix.id, { running: true, line: '', error: '' })
    let off = () => {}
    const final = new Promise(resolve => {
      off = subscribeEvent('health:fixProgress', ev => {
        if (!ev || ev.fix_id !== fix.id) return
        if (ev.kind === 'line') { patchFix(fix.id, { line: ev.message || '' }); return }
        resolve(ev)
      })
    })
    offs.current.add(off)
    const r = await callStep(() => RunHealthFix(fix.id))
    const attach = !r.ok && !r.loginRequired && ALREADY.test(r.message)
    const ev = r.ok || attach ? await final : { kind: 'error', message: r.loginRequired ? t('onboarding.shell.signIn') : r.message }
    off()
    offs.current.delete(off)
    if (!alive.current) return
    if (ev.kind === 'done') {
      patchFix(fix.id, { running: false, line: '', error: '' })
      check()
      return
    }
    const message = ev.cancelled ? t('onboarding.system.fix.cancelled') : (ev.message || t('onboarding.system.fix.failed'))
    patchFix(fix.id, { running: false, line: '', error: message })
  }

  const allPass = !checking && !!results && ROWS.every(d => results[d.id] && d.passes(results[d.id].status))

  const markOnce = useCallback(async () => {
    if (marked.current) return true
    const r = await mark('system')
    if (r && r.ok === false) {
      if (alive.current) setNotice(r.loginRequired ? t('onboarding.shell.signIn') : r.message)
      return false
    }
    marked.current = true
    return true
  }, [mark, t])

  useEffect(() => {
    setCanContinue(allPass)
    if (allPass) markOnce()
  }, [allPass, setCanContinue, markOnce])

  useEffect(() => { setOnContinue(markOnce) })

  const anyFixRunning = Object.values(fixes).some(f => f.running)

  const rows = ROWS.map(def => {
    const res = results?.[def.id]
    const fix = res?.fix
    const fs = fix ? fixes[fix.id] : null
    let cls = ''
    let dot = <SvgIcon d={DOT} />
    let word = t('onboarding.system.status.notChecked')
    let detail = rowNote(t, def.key)
    let extra = null
    let action = null
    if (fs?.running) {
      cls = 'busy'
      dot = <span className="onb-spinner" aria-hidden="true" />
      word = fixWords(t, fix.id)[1]
      if (fs.line) detail = fs.line
    } else if (checking) {
      cls = 'busy'
      dot = <span className="onb-spinner" aria-hidden="true" />
      word = t('onboarding.system.status.checking')
    } else if (!results) {
      if (cliMissing && def.key === 'cli') {
        cls = 'bad'
        dot = <SvgIcon d={CROSS} />
        word = t('onboarding.system.status.notFound')
      }
    } else if (res && def.passes(res.status)) {
      cls = 'ok'
      dot = <SvgIcon d={CHECK} />
      word = okWord(t, def.key)
    } else if (res && res.status !== 'skip') {
      cls = 'bad'
      dot = <SvgIcon d={CROSS} />
      word = def.absent(res) ? absentWord(t, def.key) : t('onboarding.system.status.attention')
      extra = res.summary
      if (fix && fix.safety === 'manual') {
        extra = (
          <>
            {res.summary}
            <span className="onb-system-manual">{t('onboarding.system.fix.manual')} <code>{fix.command || fix.label}</code></span>
          </>
        )
      } else if (fix) {
        action = (
          <button type="button" className="onb-btn sm" onClick={() => runFix(fix)}>{fixWords(t, fix.id)[0]}</button>
        )
      }
    } else if (res) {
      extra = res.summary // skipped: a check it depends on failed
    }
    return (
      <div key={def.id} role="listitem" className={`onb-row ${cls}`.trim()}>
        <span className="onb-dot">{dot}</span>
        <div className="onb-row-t">
          <b>{rowTitle(t, def.key)}</b>
          {detail && <span>{detail}</span>}
          {extra && !fs?.running && <span className="onb-system-extra">{extra}</span>}
          {fs?.error && !fs.running && <span className="onb-system-err">{fs.error}</span>}
        </div>
        {action}
        <span className="onb-row-st">{word}</span>
      </div>
    )
  })

  const summary = checking ? t('onboarding.system.checking')
    : allPass ? t('onboarding.system.ready')
      : results ? t('onboarding.system.todo') : ''

  return (
    <div className="onb-system">
      <p className="onb-lead">{t('onboarding.system.lead')}</p>
      <div className="onb-rows" role="list" aria-label={t('onboarding.system.listLabel')}>{rows}</div>
      {notice && <p className="onb-error" role="alert">{notice}</p>}
      <div className="onb-system-foot">
        {summary && <p className="onb-muted" role="status">{summary}</p>}
        {!checking && !allPass && (
          <button type="button" className="onb-btn sm ghost" onClick={check} disabled={anyFixRunning}>{t('onboarding.system.again')}</button>
        )}
      </div>
    </div>
  )
}

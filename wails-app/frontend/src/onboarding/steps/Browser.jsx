import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { GetActiveProfile, GetExtensionInfo, InstallExtension, PairExtension, GetExtensionStatus, GetBrowsers, AssignBrowser } from '../../wailsjs/go/main/App'
import { onboarding } from '../../services/onboarding.js'
import { runFix } from '../../lib/health.js'
import { parsePairResult } from '../../components/settings/PairBrowser.jsx'
import { callStep } from '../callStep.js'
import { statusesOf } from '../steps.js'
import { Problem } from './StepProblem.jsx'
import { InstallSteps, ProfileDiagram } from './BrowserGuide.jsx'
import './Browser.css'

// Step "Connect your browser", in five sub-steps: pick a Chromium browser
// (DetectChromiumBrowsers), open the browser profile that belongs to this
// MonoAgent profile (OpenChromiumBrowser), load the extension
// (GetExtensionInfo gives the folder; InstallExtension unpacks it only when
// it is missing or old, or on request), pair it (GetExtensionStatus,
// PairExtension; the background service is started with the
// services.daemon.start fix when it is not running), then bind the
// attached browser to this profile (GetBrowsers, AssignBrowser). The step
// is done when the state says so: Go derives it from pairing + binding, so
// after AssignBrowser the step only re-reads the state.

export const PAIR_POLL_MS = 3000
const DAEMON_START = 'services.daemon.start'
const DAEMON_RESTART = 'services.daemon.restart'
const SUBS = ['pick', 'open', 'install', 'pair', 'confirm']

// The bridge as `extension status --json` reports it. Connected needs this
// client's pairing to be "ok", as Go's done does (browserRunsProfile): a
// bridge too old to tell ("unknown") counts as not paired.
export function bridgeOf(st) {
  if (!st?.running) return 'off'
  if (st.clientPairing === 'mismatch' || st.bridge?.status === 'unpaired') return 'unpaired'
  if (st.bridge?.status !== 'connected') return 'waiting'
  return st.clientPairing === 'ok' ? 'connected' : 'unpaired'
}

const browserTitle = (t, b) => `${b.label || t('onboarding.browser.confirm.unnamed')} · ${String(b.instance).slice(0, 8)}`

const PointerIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
    <circle cx="12" cy="12" r="9" /><path d="M12 11v5M12 8h.01" />
  </svg>
)

function subName(t, id) {
  switch (id) {
    case 'pick': return t('onboarding.browser.sub.pick')
    case 'open': return t('onboarding.browser.sub.open')
    case 'install': return t('onboarding.browser.sub.install')
    case 'pair': return t('onboarding.browser.sub.pair')
    default: return t('onboarding.browser.sub.confirm')
  }
}

export default function Browser({ state, profileId, reload, setCanContinue }) {
  const { t } = useTranslation()
  const [browsers, setBrowsers] = useState(null)
  const [detectErr, setDetectErr] = useState(null)
  const [profileName, setProfileName] = useState('')
  const [choice, setChoice] = useState(null)
  const [sub, setSub] = useState(0)
  const [opening, setOpening] = useState(false)
  const [openErr, setOpenErr] = useState(null)
  const [info, setInfo] = useState(null)
  const [installing, setInstalling] = useState(false)
  const [installErr, setInstallErr] = useState(null)
  const [assigned, setAssigned] = useState('')
  const done = statusesOf(state).browser === 'done'

  useEffect(() => { setCanContinue(done || !!assigned) }, [done, assigned, setCanContinue])

  useEffect(() => {
    let live = true
    callStep(() => onboarding.detectBrowsers()).then((r) => {
      if (!live) return
      const list = r.ok && Array.isArray(r.value) ? r.value : []
      setBrowsers([...list.filter((b) => b.installed), ...list.filter((b) => !b.installed)])
      setDetectErr(r.ok ? null : r)
    })
    callStep(() => GetActiveProfile()).then((r) => { if (live && r.ok && r.value?.name) setProfileName(r.value.name) })
    return () => { live = false }
  }, [])

  // prepare reads where the extension is unpacked (GetExtensionInfo, read
  // only) and unpacks it (InstallExtension) only when it is missing or not
  // the copy this app ships: unpacking replaces a folder the browser may
  // have loaded. force (Reinstall) unpacks it anyway.
  const prepare = useCallback(async (force = false) => {
    setInstalling(true)
    setInstallErr(null)
    if (!force) {
      const r = await callStep(() => GetExtensionInfo())
      if (!r.ok) { setInstalling(false); setInstallErr(r); return }
      if (r.value?.installed && r.value?.current && r.value?.path) { setInstalling(false); setInfo(r.value); return }
    }
    const r = await callStep(() => InstallExtension())
    setInstalling(false)
    if (r.ok) setInfo(r.value)
    else setInstallErr(r)
  }, [])
  // It runs when the install sub-step first opens; Try again runs it again
  // after a failure.
  useEffect(() => {
    if (sub === 2 && !info && !installing && !installErr) prepare()
  }, [sub, info, installing, installErr, prepare])

  const profile = profileName || t('onboarding.browser.thisProfile')
  const other = choice?.id === 'other'
  const OTHER = { id: 'other', name: t('onboarding.browser.other') }

  const choose = (b) => { setChoice(b); setOpenErr(null); setSub(1) }
  const open = async () => {
    setOpening(true)
    setOpenErr(null)
    const r = await callStep(() => onboarding.openBrowser(choice.id))
    setOpening(false)
    if (!r.ok) setOpenErr(r)
  }

  return (
    <div className="onb-browser">
      <p className="onb-lead">{t('onboarding.browser.lead')}</p>
      <ol className="onb-browser-sub" aria-label={t('onboarding.browser.subLabel')}>
        {SUBS.map((id, k) => {
          const text = t('onboarding.browser.subItem', { n: k + 1, name: subName(t, id) })
          return (
            <li key={id} className={k < sub ? 'done' : k === sub ? 'on' : ''} aria-current={k === sub ? 'step' : undefined}>
              {k < sub ? <button type="button" onClick={() => setSub(k)}>{text}</button> : text}
            </li>
          )
        })}
      </ol>

      {sub === 0 && (
        <>
          <p className="onb-browser-pick" id="onb-browser-pick">{t('onboarding.browser.pickLead')}</p>
          <Problem r={detectErr} />
          {browsers && (
            <div className="onb-browser-choice" role="radiogroup" aria-labelledby="onb-browser-pick">
              {[...browsers, OTHER].map((b) => (
                <button key={b.id} type="button" role="radio" className="onb-opt" aria-checked={choice?.id === b.id} onClick={() => choose(b)}>
                  <span className="onb-browser-mark" aria-hidden="true">{b.id === 'other' ? '?' : b.name[0]}</span>
                  <div>
                    <b>{b.name}</b>
                    {b.installed
                      ? <span className="onb-tag ok">{t('onboarding.browser.installed')}</span>
                      : b.id !== 'other' && <span>{t('onboarding.browser.notFound')}</span>}
                  </div>
                </button>
              ))}
            </div>
          )}
        </>
      )}

      {sub === 1 && choice && (
        <>
          <p>{other
            ? t('onboarding.browser.openOther', { profile })
            : t('onboarding.browser.openLead', { browser: choice.name, profile })}</p>
          <ProfileDiagram profile={profile} />
          <div className="onb-browser-actions">
            {!other && choice.installed && (
              <button type="button" className="onb-btn" disabled={opening} onClick={open}>
                {opening ? t('onboarding.browser.opening') : t('onboarding.browser.openButton', { browser: choice.name })}
              </button>
            )}
            <button type="button" className="onb-btn" onClick={() => setSub(2)}>{t('onboarding.browser.next')}</button>
          </div>
          <Problem r={openErr} />
        </>
      )}

      {sub === 2 && choice && (
        <InstallSteps browser={choice} info={info} installing={installing} problem={installErr}
          onRetry={() => prepare()} onReinstall={() => prepare(true)} onLoaded={() => setSub(3)} />
      )}

      {sub === 3 && <PairStep onNext={() => setSub(4)} />}

      {sub === 4 && (
        <ConfirmStep profileId={profileId} profile={profile} reload={reload} assigned={assigned} onAssigned={setAssigned} />
      )}

      <p className="onb-pointer"><PointerIcon /><span>{t('onboarding.browser.pointer')}</span></p>
    </div>
  )
}

// PairStep follows `extension status` while it is on screen: no background
// service (Start runs the daemon fix in place; if the service then runs but
// the bridge still does not answer, the status hint and Restart instead of
// Start again), not paired or waiting (Pair extension), connected (Next).
// The next read is scheduled only after the previous answer, and an answer
// older than the newest read is dropped.
export function PairStep({ onNext }) {
  const { t } = useTranslation()
  const [st, setSt] = useState(null)
  const [stErr, setStErr] = useState(null)
  const [starting, setStarting] = useState(false)
  const [started, setStarted] = useState(false)
  const [lines, setLines] = useState([])
  const [startErr, setStartErr] = useState(null)
  const [pairing, setPairing] = useState(false)
  const [pairRes, setPairRes] = useState(null)
  const live = useRef(true)
  const seq = useRef(0)
  const timer = useRef(null)

  const read = useCallback(async () => {
    clearTimeout(timer.current)
    const mine = ++seq.current
    const r = await callStep(() => GetExtensionStatus())
    if (!live.current || mine !== seq.current) return
    if (r.ok) { setSt(r.value); setStErr(null) } else setStErr(r)
    const stop = r.ok ? bridgeOf(r.value) === 'connected' : r.loginRequired
    if (!stop) timer.current = setTimeout(read, PAIR_POLL_MS)
  }, [])
  useEffect(() => {
    live.current = true
    read()
    return () => { live.current = false; clearTimeout(timer.current) }
  }, [read])

  // run applies the daemon fix (start, or restart once started did not
  // bring the bridge up) with its streamed lines, then reads the status.
  const run = async (fix) => {
    setStarting(true)
    setLines([])
    setStartErr(null)
    const r = await callStep(async () => {
      const res = await runFix(fix, (line) => { if (live.current) setLines((l) => [...l, line]) })
      if (!res.ok) throw new Error(res.message)
    })
    if (!live.current) return
    setStarting(false)
    if (r.ok) setStarted(true)
    else setStartErr(r)
    await read()
  }

  const pair = async () => {
    setPairing(true)
    setPairRes(null)
    const r = await callStep(async () => ({ pair: parsePairResult(await PairExtension()) }))
    if (!live.current) return
    setPairing(false)
    if (!r.ok) setPairRes({ fail: r })
    else if (r.value.pair.login_required) setPairRes({ fail: { ok: false, loginRequired: true } })
    else setPairRes(r.value.pair)
    read()
  }

  const bridge = st ? bridgeOf(st) : null
  return (
    <div className="onb-browser-pair">
      {!st && !stErr && (
        <p className="onb-browser-busy" role="status"><span className="onb-spinner" aria-hidden="true" />{t('onboarding.browser.pair.checking')}</p>
      )}
      <Problem r={stErr} />
      {stErr && (
        <button type="button" className="onb-btn sm" onClick={read}>{t('onboarding.browser.pair.again')}</button>
      )}

      {bridge === 'off' && !started && (
        <div className="onb-browser-off" role="status">
          <p><b>{t('onboarding.browser.pair.off')}</b> {t('onboarding.browser.pair.offWhy')}</p>
          <button type="button" className="onb-btn" disabled={starting} aria-busy={starting || undefined} onClick={() => run(DAEMON_START)}>
            {starting ? t('onboarding.browser.pair.starting') : t('onboarding.browser.pair.start')}
          </button>
        </div>
      )}
      {bridge === 'off' && started && (
        <div className="onb-browser-off" role="status">
          <p><b>{t('onboarding.browser.pair.stuck')}</b> {t('onboarding.browser.pair.restartWhy')}</p>
          {st.hint && <p className="onb-hint">{st.hint}</p>}
          <div className="onb-browser-actions">
            <button type="button" className="onb-btn" disabled={starting} aria-busy={starting || undefined} onClick={() => run(DAEMON_RESTART)}>
              {starting ? t('onboarding.browser.pair.restarting') : t('onboarding.browser.pair.restart')}
            </button>
            <button type="button" className="onb-btn ghost" disabled={starting} onClick={read}>{t('onboarding.browser.pair.again')}</button>
          </div>
        </div>
      )}
      {lines.length > 0 && <ul className="onb-log">{lines.map((l, i) => <li key={i}>{l}</li>)}</ul>}
      <Problem r={startErr} />

      {(bridge === 'waiting' || bridge === 'unpaired') && (
        <>
          <p role="status">{bridge === 'unpaired' ? t('onboarding.browser.pair.unpaired') : t('onboarding.browser.pair.waiting')}</p>
          <p>{t('onboarding.browser.pair.lead')}</p>
          <button type="button" className="onb-btn" disabled={pairing} onClick={pair}>
            {pairing ? t('onboarding.browser.pair.pairing') : t('onboarding.browser.pair.button')}
          </button>
          {pairRes?.fail && <Problem r={pairRes.fail} />}
          {pairRes && !pairRes.fail && (pairRes.opened
            ? <p className="onb-hint" role="status">{t('onboarding.browser.pair.opened')}</p>
            : <>
                {pairRes.error && <p className="onb-error" role="alert">{pairRes.error}</p>}
                {pairRes.hint && <p className="onb-hint">{pairRes.hint}</p>}
              </>)}
        </>
      )}

      {bridge === 'connected' && (
        <>
          <p className="onb-browser-ok" role="status">{t('onboarding.browser.pair.connected')}</p>
          <button type="button" className="onb-btn" onClick={onNext}>{t('onboarding.browser.next')}</button>
        </>
      )}
    </div>
  )
}

// UsedBy names the MonoAgent profiles a browser already runs (an older CLI
// reports only the first).
function UsedBy({ b }) {
  const { t } = useTranslation()
  const names = b.profile_names?.length ? b.profile_names : b.profile_name ? [b.profile_name] : []
  if (!names.length) return null
  return <span className="onb-browser-usedby">{t('onboarding.browser.confirm.usedBy', { names: names.join(', ') })}</span>
}

// ConfirmStep binds an attached browser to this MonoAgent profile: one
// attached browser is shown for confirmation, several as a list.
function ConfirmStep({ profileId, profile, reload, assigned, onAssigned }) {
  const { t } = useTranslation()
  const [rows, setRows] = useState(null)
  const [legacy, setLegacy] = useState(false)
  const [loadErr, setLoadErr] = useState(null)
  const [sel, setSel] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveErr, setSaveErr] = useState(null)

  const load = useCallback(async () => {
    setLoadErr(null)
    const r = await callStep(() => GetBrowsers())
    if (!r.ok) { setLoadErr(r); setRows([]); return }
    const all = r.value?.browsers || []
    const usable = all.filter((b) => !b.legacy)
    setLegacy(usable.length < all.length)
    setRows(usable)
    setSel((s) => (usable.length === 1 ? usable[0].instance : usable.some((b) => b.instance === s) ? s : ''))
  }, [])
  useEffect(() => { load() }, [load])

  const confirm = async () => {
    setSaving(true)
    setSaveErr(null)
    const r = await callStep(() => AssignBrowser(profileId, sel))
    if (!r.ok) { setSaving(false); setSaveErr(r); return }
    await reload()
    setSaving(false)
    const b = rows.find((x) => x.instance === sel)
    onAssigned(b ? browserTitle(t, b) : sel)
  }

  if (!rows) {
    return <p className="onb-browser-busy" role="status"><span className="onb-spinner" aria-hidden="true" />{t('onboarding.browser.confirm.loading')}</p>
  }
  return (
    <div className="onb-browser-confirm">
      <p>{t('onboarding.browser.confirm.lead', { profile })}</p>
      <Problem r={loadErr} />
      {rows.length === 0 && !loadErr && <p className="onb-muted">{t('onboarding.browser.confirm.none')}</p>}
      {legacy && <p className="onb-hint">{t('onboarding.browser.confirm.legacy')}</p>}
      {rows.length === 1 && (
        <div className="onb-card"><b>{browserTitle(t, rows[0])}</b><UsedBy b={rows[0]} /></div>
      )}
      {rows.length > 1 && (
        <div className="onb-rows" role="radiogroup" aria-label={t('onboarding.browser.confirm.listLabel')}>
          {rows.map((b) => (
            <button key={b.instance} type="button" role="radio" className="onb-opt" aria-checked={sel === b.instance} onClick={() => setSel(b.instance)}>
              <div><b>{browserTitle(t, b)}</b>{b.version && <span>{t('onboarding.browser.confirm.version', { version: b.version })}</span>}<UsedBy b={b} /></div>
            </button>
          ))}
        </div>
      )}
      <div className="onb-browser-actions">
        {rows.length > 0 && (
          <button type="button" className="onb-btn primary" disabled={!sel || saving} onClick={confirm}>
            {saving ? t('onboarding.browser.confirm.saving') : t('onboarding.browser.confirm.use')}
          </button>
        )}
        <button type="button" className="onb-btn ghost" onClick={load}>{t('onboarding.browser.confirm.again')}</button>
      </div>
      <Problem r={saveErr} />
      {assigned && <p className="onb-browser-ok" role="status">{t('onboarding.browser.confirm.done', { profile, browser: assigned })}</p>}
    </div>
  )
}

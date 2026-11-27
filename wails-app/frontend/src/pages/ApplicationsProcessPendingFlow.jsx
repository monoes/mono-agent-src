import { useState } from 'react'
import { UnsandboxedWarning } from '../components/chat/ChatWarnings.jsx'
import { useTranslation } from 'react-i18next'
import * as WailsApp from '../wailsjs/go/main/App'
import { confirm } from '../components/ConfirmDialog.jsx'
import SendToCoderButton from '../components/SendToCoderButton.jsx'
import ApplyWithCoderButton from '../components/ApplyWithCoderButton.jsx'
import { selectionText } from '../components/SendToCoderApi.js'
import { useDialog } from './connections/ui.jsx'
import t from '../lib/i18nOr.js'

// computeNextStep is the flow's per-item state-machine decision function,
// extracted pure and exported for direct unit testing (see
// ApplicationsProcessPendingFlow.test.js) without rendering the
// component. Mirrors Vault.jsx's filterAndSortEntries convention.
//
// - "auto" mode always proceeds through evaluate -> apply -> ready-to-send
//   with no per-item pause.
// - "confirm" mode pauses after evaluate ("await-decision") until the
//   caller supplies one of "apply" | "skip" | "not-interested".
// - An application already fully prepared (alreadyPrepared) in a prior
//   run skips straight to "ready-to-send" without re-invoking apply,
//   avoiding a duplicate browser tab.
export function computeNextStep({ mode, stage, decision, alreadyPrepared }) {
  if (stage === 'evaluated') {
    if (alreadyPrepared) return 'ready-to-send'
    if (mode === 'auto') return 'apply'
    if (!decision) return 'await-decision'
    if (decision === 'apply') return 'apply'
    if (decision === 'skip') return 'next'
    if (decision === 'not-interested') return 'cancel'
  }
  if (stage === 'applied') return 'ready-to-send'
  return 'next'
}

const boxStyle = {
  background: 'var(--surface, #0d1520)', border: '1px solid rgba(255,255,255,0.1)',
  borderRadius: 10, padding: 20, width: 480, fontFamily: 'var(--font-mono)',
}
const btnStyle = {
  padding: '7px 14px', borderRadius: 6, cursor: 'pointer', fontSize: 12,
  background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)', color: '#00b4d8',
}

export default function ApplicationsProcessPendingFlow({ pendingApplications, onClose }) {
  useTranslation()
  const [mode, setMode] = useState('confirm')
  const [started, setStarted] = useState(false)
  const [cvPath, setCvPath] = useState('')
  const [letterPath, setLetterPath] = useState('')
  const [index, setIndex] = useState(0)
  const [stage, setStage] = useState('idle') // idle | evaluating | evaluated | applying | applied | done
  const [verdict, setVerdict] = useState(null)
  const [applyResult, setApplyResult] = useState(null)
  const [summary, setSummary] = useState({ applied: 0, skipped: 0, notInterested: 0, sent: 0 })
  const [error, setError] = useState(null)
  // Guards Apply/Skip/Not Interested/Send Now/Next against a double-click
  // firing the same mutating call twice before React re-renders the
  // buttons away -- e.g. two concurrent ApplyToApplication calls would
  // open two duplicate browser windows (OpenForApplication is not
  // idempotent), and two concurrent SendApplication calls can race
  // Store.SetStatus's read-then-write (no compare-and-swap), surfacing a
  // confusing "invalid transition" error right after a successful send.
  // Mirrors Sidebar.jsx's movingProfileID guard for the same class of bug.
  const [busy, setBusy] = useState(false)

  const dialog = useDialog(onClose)
  const current = pendingApplications[index]

  const pickCv = async () => { const p = await WailsApp.OpenJSONFilePicker(t('ui6PagesApps.pick_cv')); if (p) setCvPath(p) }
  const pickLetter = async () => { const p = await WailsApp.OpenJSONFilePicker(t('ui6PagesApps.pick_letter')); if (p) setLetterPath(p) }

  const start = async () => {
    if (!(await confirm(t('ui6PagesApps.process_question', { n: pendingApplications.length }), { title: t('ui6PagesApps.process_pending'), confirmLabel: t('ui6PagesApps.start'), danger: false }))) return
    setStarted(true)
    processCurrent()
  }

  const finishItem = (key) => setSummary(s => ({ ...s, [key]: s[key] + 1 }))

  const advance = () => {
    if (index + 1 >= pendingApplications.length) { setStage('done'); return }
    setIndex(i => i + 1)
    setStage('idle')
    setVerdict(null)
    setApplyResult(null)
    processCurrent(index + 1)
  }

  const processCurrent = async (i = index) => {
    const item = pendingApplications[i]
    if (!item) { setStage('done'); return }
    setStage('evaluating')
    setError(null)
    try {
      const v = await WailsApp.EvaluateApplication(item.id, '')
      setVerdict(v)
      const alreadyPrepared = await WailsApp.HasGeneratedDocuments(item.id)
      const next = computeNextStep({ mode, stage: 'evaluated', alreadyPrepared })
      setStage('evaluated')
      if (next === 'apply') { applyCurrent(item) }
      else if (next === 'ready-to-send') { setApplyResult({}); setStage('applied') }
      // 'await-decision' -- stay in 'evaluated' stage, wait for a button click below.
    } catch (e) {
      setError(String(e))
    }
  }

  const applyCurrent = async (item) => {
    setBusy(true)
    setStage('applying')
    try {
      const cvData = cvPath ? await WailsApp.ReadJSONFile(cvPath) : {}
      const letterData = letterPath ? await WailsApp.ReadJSONFile(letterPath) : {}
      const result = await WailsApp.ApplyToApplication(item.id, cvData, letterData)
      setApplyResult(result)
      setStage('applied')
      finishItem('applied')
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const decide = (decision) => {
    if (busy) return
    const next = computeNextStep({ mode, stage: 'evaluated', decision })
    if (next === 'apply') applyCurrent(current)
    else if (next === 'cancel') {
      setBusy(true)
      WailsApp.SetApplicationStatus(current.id, 'cancelled', '')
        .then(() => { finishItem('notInterested'); advance() })
        .catch(e => setError(String(e)))
        .finally(() => setBusy(false))
    } else if (next === 'next') {
      finishItem('skipped')
      advance()
    }
  }

  const sendCurrent = async () => {
    if (busy) return
    setBusy(true)
    try {
      await WailsApp.SendApplication(current.id, '')
      finishItem('sent')
      // Only advance past this item once the send actually succeeded --
      // advance() -> processCurrent() clears `error` immediately (and once
      // `stage` becomes 'done' the error block stops rendering at all), so
      // calling it unconditionally after a failed SendApplication used to
      // hide the failure from the user and count the item as neither sent
      // nor skipped. On failure we stay in the 'applied' stage below, where
      // "Send Now" (retry) and "Next (send later)" (manual skip) are still
      // available.
      advance()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="modal-overlay" style={{ position: 'fixed', inset: 0, zIndex: 9500, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.6)' }}>
      <div {...dialog} role="dialog" aria-modal="true" aria-labelledby="process-pending-title" style={boxStyle}>
        <h3 id="process-pending-title" style={{ margin: 0, fontSize: 14 }}>{t('ui6PagesApps.process_title')}</h3>

        {!started && (
          <>
            <div style={{ display: 'flex', gap: 12, marginTop: 12, fontSize: 12 }}>
              <label><input type="radio" checked={mode === 'confirm'} onChange={() => setMode('confirm')} /> {t('ui6PagesApps.confirm_each')}</label>
              <label><input type="radio" checked={mode === 'auto'} onChange={() => setMode('auto')} /> {t('ui6PagesApps.auto_mode')}</label>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 12 }}>
              <button style={btnStyle} onClick={pickCv}>{cvPath ? t('ui6PagesApps.cv_path', { path: cvPath }) : t('ui6PagesApps.select_cv')}</button>
              <button style={btnStyle} onClick={pickLetter}>{letterPath ? t('ui6PagesApps.letter_path', { path: letterPath }) : t('ui6PagesApps.select_letter')}</button>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 16 }}>
              <button style={btnStyle} onClick={onClose}>{t('ui6PagesApps.cancel')}</button>
              <button style={{ ...btnStyle, background: 'rgba(16,185,129,0.15)', border: '1px solid rgba(16,185,129,0.4)', color: '#10b981' }} onClick={start}>{t('ui6PagesApps.start')}</button>
            </div>
          </>
        )}

        {started && stage !== 'done' && current && (
          <div style={{ marginTop: 12, fontSize: 12 }}>
            <div style={{ color: 'var(--text-muted)' }}>{index + 1} / {pendingApplications.length}</div>
            <div style={{ fontWeight: 600, marginTop: 4 }}>{current.title} — {current.company}</div>
            {stage === 'evaluating' && <div style={{ marginTop: 8 }}>{t('ui6PagesApps.evaluating')}</div>}
            {verdict && (
              <div style={{ marginTop: 8, padding: 8, background: 'rgba(255,255,255,0.04)', borderRadius: 6 }}>
                <div><strong>{verdict.verdict}</strong> {t('ui6PagesApps.overall', { score: verdict.overall_score?.toFixed(1) ?? '' })}</div>
                <div style={{ color: 'var(--text-muted)', marginTop: 4 }}>{verdict.rationale}</div>
                {verdict.sandbox_status && <UnsandboxedWarning />}
                <div style={{ marginTop: 8 }}>
                  <SendToCoderButton label={t('ui6PagesApps.discuss')} goal="Discuss whether this application fits me, and what would make it stronger."
                    applicationId={current.id} origin={{ page: 'applications', label: t('ui6PagesApps.applications') }}
                    context={[selectionText(`${current.title} at ${current.company}\nVerdict: ${verdict.verdict} (overall ${verdict.overall_score ?? '?'})\n${verdict.rationale || ''}`)]} />
                </div>
              </div>
            )}
            {stage === 'evaluated' && mode === 'confirm' && (
              <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
                <button style={btnStyle} disabled={busy} onClick={() => decide('apply')}>{t('ui6PagesApps.apply')}</button>
                <ApplyWithCoderButton applicationId={current.id} className="" style={btnStyle} />
                <button style={btnStyle} disabled={busy} onClick={() => decide('skip')}>{t('ui6PagesApps.skip')}</button>
                <button style={{ ...btnStyle, color: '#ef4444', border: '1px solid rgba(239,68,68,0.3)' }} disabled={busy} onClick={() => decide('not-interested')}>{t('ui6PagesApps.not_interested')}</button>
              </div>
            )}
            {stage === 'applying' && <div style={{ marginTop: 8 }}>{t('ui6PagesApps.preparing')}</div>}
            {stage === 'applied' && (
              <div style={{ marginTop: 12 }}>
                <div style={{ color: '#10b981' }}>{t('ui6PagesApps.ready_send')}</div>
                <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
                  <button style={{ ...btnStyle, background: 'rgba(16,185,129,0.15)', border: '1px solid rgba(16,185,129,0.4)', color: '#10b981' }} disabled={busy} onClick={sendCurrent}>{t('ui6PagesApps.send_now')}</button>
                  <button style={btnStyle} disabled={busy} onClick={advance}>{t('ui6PagesApps.next_later')}</button>
                </div>
              </div>
            )}
            {error && <div style={{ color: '#ff6b6b', marginTop: 8 }}>{error}</div>}
            {error && (stage === 'evaluating' || stage === 'applying') && (
              // 'evaluating'/'applying' render no buttons of their own while
              // in flight -- if the underlying call throws, stage never
              // advances past them, so without this the modal is a dead
              // end (no backdrop-click-to-close or Escape handler either).
              // Give the user an explicit way out of a failed item.
              <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
                <button style={btnStyle} onClick={() => { setError(null); finishItem('skipped'); advance() }}>{t('ui6PagesApps.skip_item')}</button>
                <button style={btnStyle} onClick={onClose}>{t('ui6PagesApps.close')}</button>
              </div>
            )}
          </div>
        )}

        {stage === 'done' && (
          <div style={{ marginTop: 12, fontSize: 12 }}>
            <div>{t('ui6PagesApps.summary_done', summary)}</div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 12 }}>
              <button style={btnStyle} onClick={onClose}>{t('ui6PagesApps.close')}</button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

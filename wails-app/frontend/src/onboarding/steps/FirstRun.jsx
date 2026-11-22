// Step 9, Your first run: the person picks a starter and it runs for real
// (useStarterRun). A card whose steps are not done says which one and offers
// to go there. Continue opens once a run has finished, failed or not; only a
// successful run marks the step. Each card says beforehand that running it adds
// the workflow to the person's list: the click is their approval to create and
// switch it on (never on mount, never on its own).
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Globe, Sparkles, AppWindow, Check, X, Clock, RotateCcw } from 'lucide-react'
import { STARTERS, firstUnmet } from '../starters.js'
import { statusesOf, stepTitle } from '../steps.js'
import { useStarterRun } from './useStarterRun.js'
import './FirstRun.css'

const ICONS = { starter_page_read: Globe, starter_summarise_page: Sparkles, starter_browser_read: AppWindow }

function starterTitle(t, id) {
  switch (id) {
    case 'starter_page_read': return t('onboarding.firstRun.starter.page.title')
    case 'starter_summarise_page': return t('onboarding.firstRun.starter.summary.title')
    case 'starter_browser_read': return t('onboarding.firstRun.starter.browser.title')
    default: return id
  }
}

function starterLine(t, id) {
  switch (id) {
    case 'starter_page_read': return t('onboarding.firstRun.starter.page.line')
    case 'starter_summarise_page': return t('onboarding.firstRun.starter.summary.line')
    case 'starter_browser_read': return t('onboarding.firstRun.starter.browser.line')
    default: return ''
  }
}

function fieldLabel(t, key) {
  switch (key) {
    case 'title': return t('onboarding.firstRun.field.title')
    case 'first_paragraph': return t('onboarding.firstRun.field.firstParagraph')
    case 'summary': return t('onboarding.firstRun.field.summary')
    case 'description': return t('onboarding.firstRun.field.description')
    case 'page': return t('onboarding.firstRun.field.page')
    default: return key
  }
}

function nodeStateLabel(t, state) {
  switch (state) {
    case 'done': return t('onboarding.firstRun.node.done')
    case 'running': return t('onboarding.firstRun.node.running')
    case 'waiting': return t('onboarding.firstRun.node.waiting')
    case 'failed': return t('onboarding.firstRun.node.failed')
    case 'skipped': return t('onboarding.firstRun.node.skipped')
    default: return t('onboarding.firstRun.node.todo')
  }
}

function reasonLine(t, reason) {
  switch (reason) {
    case 'approval': return t('onboarding.firstRun.approval')
    case 'agent': return t('onboarding.firstRun.agentNotSetup')
    case 'notStarted': return t('onboarding.firstRun.notStarted')
    case 'lost': return t('onboarding.firstRun.lost')
    case 'noText': return t('onboarding.firstRun.noErrorText')
    default: return ''
  }
}

function NodeIcon({ state }) {
  if (state === 'running') return <span className="onb-spinner" aria-hidden="true" />
  if (state === 'done') return <Check aria-hidden="true" />
  if (state === 'failed') return <X aria-hidden="true" />
  if (state === 'waiting') return <Clock aria-hidden="true" />
  return <span className="onb-fr-pip" aria-hidden="true" />
}

function Pipeline({ nodes }) {
  const { t } = useTranslation()
  return (
    <ol className="onb-fr-pipe" aria-label={t('onboarding.firstRun.progress')}>
      {nodes.map(n => (
        <li key={n.id} className={`onb-fr-node ${n.state}`} aria-label={t('onboarding.firstRun.nodeState', { name: n.name, state: nodeStateLabel(t, n.state) })}>
          <i><NodeIcon state={n.state} /></i>
          <span aria-hidden="true">{n.name}</span>
        </li>
      ))}
    </ol>
  )
}

function StatusLine({ run }) {
  const { t } = useTranslation()
  if (run.phase === 'preparing') return <p className="onb-muted" aria-live="polite">{t('onboarding.firstRun.preparing')}</p>
  if (run.phase === 'starting') return <p className="onb-muted" aria-live="polite">{t('onboarding.firstRun.starting')}</p>
  if (run.phase !== 'running') return null
  const at = run.nodes.find(n => n.state === 'running')
  return <p className="onb-muted" aria-live="polite">{at ? t('onboarding.firstRun.running', { name: at.name }) : t('onboarding.firstRun.started')}</p>
}

export default function FirstRun({ state, mark, goTo, setCanContinue }) {
  const { t } = useTranslation()
  const statuses = statusesOf(state)
  const [finished, setFinished] = useState(false)
  const [markError, setMarkError] = useState('')
  const { run, start, busy } = useStarterRun({
    onSuccess: async () => {
      const r = await mark('firstRun')
      if (r && r.ok === false) setMarkError(r.loginRequired ? t('onboarding.shell.signIn') : r.message)
    },
  })
  const over = run && (run.phase === 'done' || run.phase === 'failed')
  useEffect(() => { if (over) setFinished(true) }, [over])
  useEffect(() => { setCanContinue(finished && !busy) }, [finished, busy, setCanContinue])

  const pick = (starter) => {
    if (busy) return
    setMarkError('')
    start(starter)
  }
  const current = run ? STARTERS.find(s => s.templateId === run.templateId) : null

  return (
    <>
      <p className="onb-lead">{t('onboarding.firstRun.lead')}</p>
      <div className="onb-fr-runs">
        {STARTERS.map(s => {
          const Icon = ICONS[s.templateId] || Globe
          const missing = firstUnmet(s, statuses)
          const body = (
            <>
              <Icon aria-hidden="true" />
              <b>{starterTitle(t, s.templateId)}</b>
              <span className="onb-fr-line">{starterLine(t, s.templateId)}</span>
              <span className="onb-fr-adds">{t('onboarding.firstRun.addsWorkflow')}</span>
            </>
          )
          if (missing) {
            const name = stepTitle(t, missing)
            return (
              <div key={s.templateId} className="onb-fr-card blocked">
                {body}
                <span className="onb-fr-need">{t('onboarding.firstRun.needs', { step: name })}</span>
                <button type="button" className="onb-btn ghost sm" onClick={() => goTo(missing)}>{t('onboarding.firstRun.goTo', { step: name })}</button>
              </div>
            )
          }
          return (
            <button key={s.templateId} type="button" className="onb-fr-card" aria-pressed={run?.templateId === s.templateId} disabled={busy} onClick={() => pick(s)}>
              {body}
            </button>
          )
        })}
      </div>

      {!run && <p className="onb-muted">{t('onboarding.firstRun.idle')}</p>}
      {run && (
        <div className="onb-fr-stage">
          {run.nodes.length > 0 && <Pipeline nodes={run.nodes} />}
          <StatusLine run={run} />
          {run.phase === 'done' && (
            <div className="onb-fr-result" role="status">
              <h2>{t('onboarding.firstRun.result')}</h2>
              {run.result.length === 0
                ? <p>{t('onboarding.firstRun.resultEmpty')}</p>
                : run.result.map(f => (
                  <p key={f.key}><span className="onb-fr-key">{fieldLabel(t, f.key)}</span><span>{f.value}</span></p>
                ))}
            </div>
          )}
          {run.phase === 'failed' && (
            <div className="onb-fr-failed">
              <p className="onb-fr-failed-h">{t('onboarding.firstRun.failed')}</p>
              {run.loginRequired
                ? <p className="onb-error" role="alert">{t('onboarding.shell.signIn')}</p>
                : run.error && <p className="onb-error" role="alert">{run.error}</p>}
              {reasonLine(t, run.reason) && <p className="onb-hint">{reasonLine(t, run.reason)}</p>}
              {current && (
                <button type="button" className="onb-btn sm" onClick={() => pick(current)}>
                  <RotateCcw aria-hidden="true" /> {t('onboarding.firstRun.retry')}
                </button>
              )}
            </div>
          )}
          {markError && <p className="onb-error" role="alert">{markError}</p>}
        </div>
      )}
    </>
  )
}

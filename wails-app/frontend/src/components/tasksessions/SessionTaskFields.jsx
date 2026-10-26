import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'

// The drawer's section for a task of a session: acceptance, dependencies,
// assignee, retries, artifacts, and the session's own recent events. All of
// it is text an agent may have written: shown as text, never acted on.
export default function SessionTaskFields({ task, artifacts = [], sessionEvents = [], focusArtifacts = false }) {
  const { t } = useTranslation()
  const head = useRef(null)
  // Asked for (the chat's report link): scroll to the artifact list and focus it once it has something to show.
  useEffect(() => {
    if (!focusArtifacts || artifacts.length === 0 || !head.current) return
    head.current.scrollIntoView?.({ block: 'start' })
    head.current.focus({ preventScroll: true })
  }, [focusArtifacts, artifacts.length])
  return (
    <div data-testid="session-fields">
      <h3 className="tb-drawer-h">{t('tasks.session.acceptance')}</h3>
      <div className="tb-note" data-testid="acceptance">{task.acceptance || t('tasks.session.noAcceptance')}</div>
      <dl className="ts-facts">
        <dt>{t('tasks.session.dependsOn')}</dt>
        <dd>{task.depends_on?.length ? task.depends_on.map(i => `#${i}`).join(', ') : t('tasks.session.none')}</dd>
        <dt>{t('tasks.session.assignee')}</dt>
        <dd>{task.assignee || t('tasks.session.none')}</dd>
        <dt>{t('tasks.session.retriesLabel')}</dt>
        <dd>{task.retries || 0}</dd>
      </dl>
      <h3 className="tb-drawer-h" ref={head} tabIndex={-1} data-testid="artifacts-head">{t('tasks.session.artifacts')}</h3>
      {artifacts.length === 0
        ? <div className="tb-empty">{t('tasks.session.noArtifacts')}</div>
        : <ul className="tb-history" data-testid="artifacts">{artifacts.map(a => (
          <li key={a.id}><strong>{a.kind}</strong> <code>{a.ref ?? a.ref_untrusted}</code>{(a.note ?? a.note_untrusted) && <div className="tb-note">{a.note ?? a.note_untrusted}</div>}</li>
        ))}</ul>}
      {sessionEvents.length > 0 && (
        <details className="tb-help">
          <summary>{t('tasks.session.events')}</summary>
          <ul className="tb-history" data-testid="session-events">{sessionEvents.slice(-15).reverse().map(e => (
            <li key={e.id}><strong>{e.actor}</strong> {e.kind}{e.note && <div className="tb-note">{e.note}</div>}</li>
          ))}</ul>
        </details>
      )}
    </div>
  )
}

import React, { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import * as WailsApp from '../../wailsjs/go/main/App'
import SendToCoderButton from '../SendToCoderButton.jsx'
import { sectionCardStyle, subtleBtnStyle } from './constants.jsx'
import { parkedApi } from './parkedApi.js'
import t from '../../lib/i18nOr.js'

const noteStyle = { fontSize: 11, color: 'var(--text-secondary, #94a3b8)', lineHeight: 1.5 }

// ApplicationParkedPanel is what the detail header shows while an application
// waits on the person. awaiting_submit: the coder filled the form and left its
// tab open; the person submits there. When the extension saw a submit in that
// tab it asks, and only Confirm (the same path as "Send", `application send`)
// moves the application to applied. needs_you: why the coder stopped, and a way
// to reopen and refill. Nothing here says an application was submitted: that is
// only true once its status is applied.
export default function ApplicationParkedPanel({ detail, assets, onChanged }) {
  useTranslation()
  const status = detail?.status
  const id = detail?.id
  const [tab, setTab] = useState(null)
  const [dismissed, setDismissed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    setDismissed(false)
    setTab(null)
    if (status !== 'awaiting_submit' || !id) return undefined
    let live = true
    parkedApi.tab(id).then(found => { if (live) setTab(found) }).catch(() => {})
    return () => { live = false }
  }, [id, status])

  if (status !== 'awaiting_submit' && status !== 'needs_you') return null

  const fill = assets?.fill || {}
  const confirm = async () => {
    setBusy(true)
    setError('')
    try {
      await parkedApi.confirm(id)
      onChanged?.()
    } catch (err) {
      setError(String(err?.message || err))
    } finally {
      setBusy(false)
    }
  }
  const openScreenshot = () => {
    try { WailsApp.OpenPathWithOS(fill.screenshot) } catch (err) { setError(String(err?.message || err)) }
  }

  if (status === 'needs_you') {
    return (
      <section aria-label={t('ui6PagesApps.needs_you_title')} style={{ ...sectionCardStyle, borderColor: 'rgba(244,63,94,0.35)' }}>
        <strong style={{ fontSize: 12, color: '#f43f5e' }}>{t('ui6PagesApps.needs_you_title')}</strong>
        <span style={noteStyle}>{fill.note || t('ui6PagesApps.coder_stopped')}</span>
        <div>
          <SendToCoderButton label={t('ui6PagesApps.reopen_button')} applicationId={id}
            start={() => parkedApi.resume(id)} doneLabel={t('ui6PagesApps.done_label')}
            origin={{ page: 'applications', label: t('ui6PagesApps.applications') }} />
        </div>
      </section>
    )
  }

  const observed = tab?.submitObservedAt && !dismissed
  return (
    <section aria-label={t('ui6PagesApps.waiting_title')} style={{ ...sectionCardStyle, borderColor: 'rgba(0,180,216,0.35)' }}>
      <strong style={{ fontSize: 12, color: '#00b4d8' }}>{t('ui6PagesApps.waiting_title')}</strong>
      <span style={noteStyle}>
        {t('ui6PagesApps.filled_stopped')}
        {tab?.tab?.origin ? ` ${t('ui6PagesApps.tab_origin', { origin: tab.tab.origin })}` : ''}
      </span>
      {fill.screenshot && (
        <div>
          <button type="button" style={subtleBtnStyle} onClick={openScreenshot}>{t('ui6PagesApps.view_screenshot')}</button>
        </div>
      )}
      {observed && (
        <div role="status" style={{ ...noteStyle, color: 'var(--text-primary, #f1f5f9)', display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <span>{t('ui6PagesApps.saw_submit')}</span>
          <button type="button" style={subtleBtnStyle} onClick={confirm} disabled={busy}>{t('ui6PagesApps.confirm')}</button>
          <button type="button" style={subtleBtnStyle} onClick={() => setDismissed(true)} disabled={busy}>{t('ui6PagesApps.not_yet')}</button>
        </div>
      )}
      {error && <span role="alert" style={{ ...noteStyle, color: '#f43f5e' }}>{error}</span>}
    </section>
  )
}

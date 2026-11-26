import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { X } from 'lucide-react'
import { useDialog } from '../pages/connections/ui.jsx'
import SendToCoderButton from './SendToCoderButton.jsx'
import { selectionText } from './SendToCoderApi.js'

export const BRIEF_MAX = 4000

// GenerateWorkflowDialog asks for a brief and hands it to the coder as a
// workflow-generate task. There is no non-coder generate path, so with coder
// mode off the button says why and links to Settings (SendToCoderButton).
export default function GenerateWorkflowDialog({ onClose }) {
  const { t } = useTranslation()
  const dialog = useDialog(onClose)
  const [brief, setBrief] = useState('')
  const text = brief.trim()
  return (
    <div className="modal-overlay" onClick={e => e.target === e.currentTarget && onClose()} style={{ zIndex: 1100 }}>
      <div {...dialog} className="modal" role="dialog" aria-modal="true" aria-labelledby="wf-generate-title" style={{ width: 520 }}>
        <div className="modal-title">
          <span id="wf-generate-title">{t('ui6ConnMisc.generate_title')}</span>
          <button className="btn btn-ghost btn-icon" onClick={onClose} aria-label={t('ui6ConnMisc.close')}><X size={15} /></button>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <label htmlFor="wf-generate-brief" style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('ui6ConnMisc.describe')}</label>
          <textarea id="wf-generate-brief" className="form-input" rows={7} maxLength={BRIEF_MAX} value={brief} required
            placeholder={t('ui6ConnMisc.brief_placeholder')}
            onChange={e => setBrief(e.target.value)} style={{ resize: 'vertical' }} />
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', justifyContent: 'flex-end' }}>
            <span data-testid="brief-count" style={{ marginRight: 'auto', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>{brief.length} / {BRIEF_MAX}</span>
            <button type="button" className="btn btn-ghost btn-sm" onClick={onClose}>{t('ui6ConnMisc.cancel')}</button>
            {text ? (
              <SendToCoderButton kind="workflow-generate" label={t('ui6ConnMisc.generate')} oneshotNote={false} className="btn btn-primary btn-sm"
                goal="Generate a workflow from the brief." context={() => [selectionText(text.slice(0, BRIEF_MAX))]}
                origin={{ page: 'noderunner', label: t('ui6ConnMisc.workflows') }} onSent={onClose} />
            ) : (
              <button type="button" className="btn btn-primary btn-sm" disabled>{t('ui6ConnMisc.generate')}</button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

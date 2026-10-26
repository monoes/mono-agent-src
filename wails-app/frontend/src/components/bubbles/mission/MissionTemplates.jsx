import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus } from 'lucide-react'
import { MISSION_TEMPLATES, insertTemplate, looksLikeMission } from '../../../lib/missionTemplates.js'
import './mission.css'

// The mission template picker: a "+" menu above the composer of a coding
// chat. A first message that looks like a big job (hint) makes it say so.
// Choosing one puts its skeleton into the composer; nothing is sent.
export default function MissionTemplates({ draft, onDraftChange, hintable = false }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const hint = hintable && looksLikeMission(draft)
  const pick = (id) => { onDraftChange(insertTemplate(draft, id)); setOpen(false) }
  return (
    <div className="ms-templates" onKeyDown={e => { if (open && e.key === 'Escape') { e.stopPropagation(); setOpen(false) } }}>
      <button type="button" className={`btn btn-ghost btn-sm${hint ? ' ms-hint' : ''}`} aria-haspopup="menu" aria-expanded={open}
        data-testid="mission-templates-btn" onClick={() => setOpen(o => !o)}>
        <Plus size={11} aria-hidden="true" /> {hint ? t('bubbles.mission.templateHint') : t('bubbles.mission.templates')}
      </button>
      {open && (
        <ul role="menu" className="ms-menu" aria-label={t('bubbles.mission.templates')}>
          {MISSION_TEMPLATES.map(m => (
            <li key={m.id} role="none"><button type="button" role="menuitem" className="btn btn-ghost btn-sm" onClick={() => pick(m.id)}>{t(`bubbles.mission.template.${m.id}`)}</button></li>
          ))}
        </ul>
      )}
    </div>
  )
}

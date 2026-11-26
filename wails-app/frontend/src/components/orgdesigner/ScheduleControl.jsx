// Schedule button for the designer toolbar (#342). Any org can run on an
// interval, a sections org included: monomind starts each tick as a fresh run
// (its own run id and, for a sections org, an empty document store), so there
// is nothing to carry forward and nothing here blocks it.
import { useState } from 'react'
import { Clock } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import en from '../../locales/en.json'
import '../../i18n.js'

const toolbarBtnStyle = {
  display: 'flex', alignItems: 'center', gap: 4,
  fontFamily: 'var(--font-mono)', fontSize: 10, padding: '4px 8px', borderRadius: 'var(--radius)',
  background: 'transparent', border: '1px solid var(--border)', color: 'var(--text-muted)', cursor: 'pointer',
}

// The English wording of the two notes (the screen shows the locale's).
export const FRESH_RUN_NOTE = en.ui6OrgsCanvas.fresh_run_note
export const SECTIONS_NOTE = en.ui6OrgsCanvas.sections_note

// monomind's schedule is "45s" | "15m" | "2h" or a number of minutes.
export function scheduleLabel(schedule) {
  if (schedule == null || schedule === '' || schedule === 0) return ''
  return typeof schedule === 'number' ? `${schedule}m` : String(schedule)
}

export default function ScheduleControl({ schedule, sections = false, onSave }) {
  const { t } = useTranslation()
  const current = scheduleLabel(schedule)
  const [open, setOpen] = useState(false)
  const [value, setValue] = useState(current)
  const [error, setError] = useState('')

  const save = async (next) => {
    const res = await onSave(next)
    if (res?.error) { setError(res.error); return }
    setError('')
    setOpen(false)
  }

  return (
    <span style={{ position: 'relative' }}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => { setValue(current); setError(''); setOpen(o => !o) }}
        title={t('ui6OrgsCanvas.schedule_title')}
        style={toolbarBtnStyle}
      >
        <Clock size={11} /> {current ? t('ui6OrgsCanvas.every', { interval: current }) : t('ui6OrgsCanvas.schedule')}
      </button>
      {open && (
        <div role="dialog" aria-label={t('ui6OrgsCanvas.schedule')} style={{
          position: 'absolute', top: '100%', right: 0, marginTop: 4, zIndex: 30, width: 280,
          padding: 10, display: 'flex', flexDirection: 'column', gap: 6,
          background: 'var(--surface, #0d1520)', border: '1px solid var(--border)', borderRadius: 8,
          boxShadow: '0 8px 24px rgba(0,0,0,0.45)', fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-secondary)',
        }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
            {t('ui6OrgsCanvas.interval_help')}
            <input
              aria-label={t('ui6OrgsCanvas.interval')}
              value={value}
              onChange={e => setValue(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') save(value) }}
              placeholder={t('ui6OrgsCanvas.interval_none')}
              style={{ fontFamily: 'inherit', fontSize: 11, padding: '3px 6px', background: 'var(--elevated)', color: 'var(--text)', border: '1px solid var(--border)', borderRadius: 4 }}
            />
          </label>
          <span data-testid="schedule-note" style={{ lineHeight: 1.5 }}>
            {t('ui6OrgsCanvas.fresh_run_note')}{sections ? ` ${t('ui6OrgsCanvas.sections_note')}` : ''} {t('ui6OrgsCanvas.tick_listed')}
          </span>
          {error && <span role="alert" style={{ color: '#f87171' }}>{error}</span>}
          <span style={{ display: 'flex', gap: 6 }}>
            <button type="button" onClick={() => save(value)} style={toolbarBtnStyle}>{t('ui6OrgsCanvas.save')}</button>
            {current && <button type="button" onClick={() => save('')} style={toolbarBtnStyle}>{t('ui6OrgsCanvas.clear')}</button>}
          </span>
        </div>
      )}
    </span>
  )
}

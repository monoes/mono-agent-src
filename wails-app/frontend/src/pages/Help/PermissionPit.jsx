import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ShieldCheck, Undo2, ArrowRight, MessageSquare } from 'lucide-react'
import { PIT_STAGES } from './helpContent.js'

// Face for a given depth (0 = nothing granted). Meaning is also in text, never motion alone.
const FACES = [
  { mouth: 'M-8 6 Q0 12 8 6', brow: 0, sweat: 0 },
  { mouth: 'M-7 7 Q0 9 7 7', brow: 2, sweat: 0 },
  { mouth: 'M-7 8 L7 8', brow: 4, sweat: 1 },
  { mouth: 'M-7 10 Q0 4 7 10', brow: 6, sweat: 2 },
  { mouth: 'M-6 6 Q0 14 6 6 Z', brow: 8, sweat: 3 },
]
const faceFor = (idx, max) => FACES[Math.min(FACES.length - 1, Math.round((idx / Math.max(1, max)) * (FACES.length - 1)))]

export function Mascot({ depth = 0, max = 5, onPoke, label, className = '' }) {
  const f = faceFor(depth, max)
  const wide = f.brow > 4
  const body = (
    <svg viewBox="-40 -40 80 80" width="96" height="96" role="img" aria-label={label} className={`help-mascot-svg d${Math.min(4, Math.round((depth / Math.max(1, max)) * 4))}`}>
      <ellipse cx="0" cy="34" rx="22" ry="4" fill="var(--border-dim)" />
      <g className="help-mascot-body">
        <rect x="-26" y="-28" width="52" height="56" rx="16" fill="var(--elevated)" stroke="var(--cyan)" strokeWidth="2" />
        <line x1="0" y1="-28" x2="0" y2="-36" stroke="var(--cyan)" strokeWidth="2" />
        <circle cx="0" cy="-37" r="3" fill="var(--teal)" className="help-mascot-bulb" />
        <circle cx="-10" cy="-6" r={wide ? 6 : 4.5} fill="var(--text)" />
        <circle cx="10" cy="-6" r={wide ? 6 : 4.5} fill="var(--text)" />
        <circle cx="-10" cy="-6" r="1.8" fill="var(--void)" className="help-mascot-pupil" />
        <circle cx="10" cy="-6" r="1.8" fill="var(--void)" className="help-mascot-pupil" />
        <line x1="-16" y1={-16} x2="-5" y2={-16 - f.brow * 0.6} stroke="var(--text-secondary)" strokeWidth="2" strokeLinecap="round" />
        <line x1="16" y1={-16} x2="5" y2={-16 - f.brow * 0.6} stroke="var(--text-secondary)" strokeWidth="2" strokeLinecap="round" />
        <path d={f.mouth} transform="translate(0 6)" fill={f.brow >= 8 ? 'var(--void)' : 'none'} stroke="var(--text)" strokeWidth="2" strokeLinecap="round" />
        {f.sweat > 0 && <path d="M20 -18 q-4 7 0 10 q4 -3 0 -10z" fill="var(--cyan-bright)" className="help-sweat" />}
        {f.sweat > 1 && <path d="M-22 -4 q-4 7 0 10 q4 -3 0 -10z" fill="var(--cyan-bright)" className="help-sweat s2" />}
        {f.sweat > 2 && <path d="M24 8 q-4 7 0 10 q4 -3 0 -10z" fill="var(--cyan-bright)" className="help-sweat s3" />}
      </g>
    </svg>
  )
  if (!onPoke) return <span className={`help-mascot ${className}`}>{body}</span>
  return <button type="button" className={`help-mascot help-mascot-btn ${className}`} onClick={onPoke}>{body}</button>
}

export default function PermissionPit({ onAction }) {
  const { t } = useTranslation()
  const stages = PIT_STAGES
  const max = stages.length
  const [depth, setDepth] = useState(0)
  const stage = depth > 0 ? stages[depth - 1] : null
  return (
    <section className={`card help-pit pit-${depth}`} aria-labelledby="help-pit-title" data-testid="help-pit">
      <div className="help-pit-top">
        <Mascot depth={depth} max={max} label={t('help.pit.mascotLabel', { n: depth })} />
        <div className="help-pit-intro">
          <h2 id="help-pit-title" className="help-pit-title">{t('help.pit.title')}</h2>
          <p className="help-pit-desc">{t('help.pit.intro')}</p>
        </div>
      </div>

      <div className="help-pit-dial">
        <label htmlFor="help-pit-range" className="help-pit-label">{t('help.pit.dialLabel')}</label>
        <input
          id="help-pit-range" type="range" min={0} max={max} step={1} value={depth}
          aria-valuetext={stage ? `${stage.level}: ${stage.label}` : t('help.pit.nothing')}
          onChange={e => setDepth(Number(e.target.value))}
        />
        <div className="help-pit-ticks" aria-hidden="true">
          {[0, ...stages.map(s => s.level)].map((l, i) => <span key={i} className={i <= depth ? 'on' : ''}>{i}</span>)}
        </div>
      </div>

      <ul className="help-pit-grants" aria-label={t('help.pit.grantsLabel')}>
        {stages.slice(0, depth).map((s, i) => (
          <li key={s.level} className="help-grant" style={{ '--i': i }}>
            <span className="help-grant-perm">{s.permission}</span>
            <span className="help-grant-label">{s.label}</span>
          </li>
        ))}
      </ul>

      <p className="help-pit-consequence" role="status" aria-live="polite" data-testid="pit-consequence">
        {stage ? stage.consequence : t('help.pit.calm')}
      </p>

      <div className="help-pit-actions">
        <button type="button" className="btn btn-sm btn-secondary" disabled={depth >= max} onClick={() => setDepth(d => Math.min(max, d + 1))}>
          {t('help.pit.grant')}
        </button>
        <button type="button" className={`btn btn-sm ${depth > 0 ? 'btn-primary' : 'btn-ghost'}`} disabled={depth === 0} onClick={() => setDepth(0)}>
          <Undo2 size={13} aria-hidden="true" /> {t('help.pit.revoke')}
        </button>
      </div>

      <p className="help-pit-safe"><ShieldCheck size={14} aria-hidden="true" /> {t('help.pit.safe')}</p>
      <div className="help-pit-actions">
        <button type="button" className="btn btn-sm btn-secondary" onClick={() => onAction({ kind: 'goto', target: 'settings' })}>
          <ArrowRight size={13} aria-hidden="true" /> {t('help.pit.review')}
        </button>
        <button type="button" className="btn btn-sm btn-secondary" onClick={() => onAction({ kind: 'coder', prompt: t('help.pit.coderPrompt') })}>
          <MessageSquare size={13} aria-hidden="true" /> {t('help.pit.askCoder')}
        </button>
      </div>
    </section>
  )
}

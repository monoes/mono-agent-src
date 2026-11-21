// Step 4, About you: a few profile Info fields (SetProfileInfo, the Info
// page's binding). Your name is full_name, and also first_name / last_name
// split on its last space; Role is job_title; Company or project is employer;
// the chosen help chips are one about_me line. Only filled fields are written:
// SetProfileInfo with an empty value would clear the field. Then the mark.
//
// The intro says Info is used for form filling: internal/profilectx (what
// agents are told about the profile) does not read profile Info; its readers
// are the form-fill paths (applyfill, the browser form-fill node, the
// extension's formfill).
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { SetProfileInfo } from '../../wailsjs/go/main/App'
import { callStep } from '../callStep.js'

// In catalogue order; about_me lists the chosen ones in this order.
const HELP = ['outreach', 'content', 'research', 'coding', 'inbox', 'operations']

function helpLabel(t, id) {
  switch (id) {
    case 'outreach': return t('onboarding.about.help.outreach')
    case 'content': return t('onboarding.about.help.content')
    case 'research': return t('onboarding.about.help.research')
    case 'coding': return t('onboarding.about.help.coding')
    case 'inbox': return t('onboarding.about.help.inbox')
    default: return t('onboarding.about.help.operations')
  }
}

const PinIcon = () => (
  <svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M12 21s-6-5.4-6-11a6 6 0 0 1 12 0c0 5.6-6 11-6 11z" /><circle cx="12" cy="10" r="2.2" />
  </svg>
)

// The entries to write, in order, leaving out every empty one.
export function infoEntries(t, { name, role, company, help }) {
  const out = []
  const full = name.trim().replace(/\s+/g, ' ')
  if (full) {
    out.push(['full_name', full])
    const cut = full.lastIndexOf(' ')
    if (cut > 0) {
      out.push(['first_name', full.slice(0, cut)])
      out.push(['last_name', full.slice(cut + 1)])
    } else {
      out.push(['first_name', full])
    }
  }
  if (role.trim()) out.push(['job_title', role.trim()])
  if (company.trim()) out.push(['employer', company.trim()])
  const chosen = HELP.filter(id => help.includes(id))
  if (chosen.length) {
    out.push(['about_me', t('onboarding.about.helpLine', { list: chosen.map(id => helpLabel(t, id)).join(', ') })])
  }
  return out
}

export default function AboutYou({ mark, setCanContinue, setOnContinue }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [role, setRole] = useState('')
  const [company, setCompany] = useState('')
  const [help, setHelp] = useState([])
  const [error, setError] = useState('')

  useEffect(() => { setCanContinue(true) }, [setCanContinue])

  useEffect(() => {
    setOnContinue(async () => {
      setError('')
      const fail = (r) => { setError(r.loginRequired ? t('onboarding.shell.signIn') : r.message); return false }
      for (const [key, value] of infoEntries(t, { name, role, company, help })) {
        const r = await callStep(() => SetProfileInfo(key, value))
        if (!r.ok) return fail(r)
      }
      const m = await mark('about')
      if (m && m.ok === false) return fail(m)
      return true
    })
  })

  const toggle = (id) => setHelp(h => (h.includes(id) ? h.filter(x => x !== id) : [...h, id]))

  return (
    <div className="onb-about">
      <p className="onb-lead">{t('onboarding.about.lead')}</p>
      <div className="onb-row2">
        <div className="onb-field">
          <label htmlFor="onb-about-name">{t('onboarding.about.name')}</label>
          <input id="onb-about-name" className="onb-input" type="text" autoComplete="name" value={name} onChange={e => setName(e.target.value)} />
        </div>
        <div className="onb-field">
          <label htmlFor="onb-about-role">{t('onboarding.about.role')}</label>
          <input id="onb-about-role" className="onb-input" type="text" autoComplete="organization-title" placeholder={t('onboarding.about.rolePlaceholder')} value={role} onChange={e => setRole(e.target.value)} />
        </div>
      </div>
      <div className="onb-field">
        <label htmlFor="onb-about-company">{t('onboarding.about.company')}</label>
        <input id="onb-about-company" className="onb-input" type="text" autoComplete="organization" value={company} onChange={e => setCompany(e.target.value)} />
      </div>
      <div className="onb-field" style={{ margin: 0 }}>
        <span className="onb-label" id="onb-about-help">{t('onboarding.about.helpLabel')}</span>
        <div className="onb-chips" role="group" aria-labelledby="onb-about-help">
          {HELP.map(id => (
            <button key={id} type="button" className="onb-chip" aria-pressed={help.includes(id)} onClick={() => toggle(id)}>
              {helpLabel(t, id)}
            </button>
          ))}
        </div>
      </div>
      {error && <p className="onb-error" role="alert">{error}</p>}
      <p className="onb-pointer"><PinIcon /><span>{t('onboarding.about.pointer')}</span></p>
    </div>
  )
}

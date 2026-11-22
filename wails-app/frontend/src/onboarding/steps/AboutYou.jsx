// Step 4, About you: a few profile Info fields (GetProfileInfo / SetProfileInfo,
// the Info page's bindings). Your name is full_name, and also first_name /
// last_name split on its last space; Role is job_title; Company or project is
// employer; the chosen help chips are one "Wants help with: …" line in about_me.
//
// The fields start from the profile's current Info (setup can run again on a
// profile that has it), and only what the person changed is written. An empty
// field is never written: SetProfileInfo with an empty value clears the field.
// The exceptions are deliberate clears: last_name when the name became one
// word, and about_me when our line was all it held.
// In about_me only our own line is replaced (in any UI language); the rest of
// the text stays as the person wrote it. Then the mark.
//
// The intro says Info is used for form filling: internal/profilectx (what
// agents are told about the profile) does not read profile Info; its readers
// are the form-fill paths (applyfill, the browser form-fill node, the
// extension's formfill).
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { GetProfileInfo, SetProfileInfo } from '../../wailsjs/go/main/App'
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

const MARK = '\u0000'
const tidy = (s) => s.trim().replace(/\s+/g, ' ')

// Our help line in every loaded language: { pre, post, labels: { label: id } },
// so a line written while the UI was in another language is still ours.
function helpLineForms(i18n) {
  const langs = Object.keys(i18n.store?.data || {})
  return (langs.length ? langs : [i18n.language]).map(lng => {
    const t = i18n.getFixedT(lng)
    const [pre, post = ''] = t('onboarding.about.helpLine', { list: MARK }).split(MARK)
    const labels = {}
    for (const id of HELP) labels[helpLabel(t, id)] = id
    return { pre, post, labels }
  })
}

// The chip ids in a line of ours, or null when the line is not ours.
function ourLine(forms, line) {
  const s = line.trim()
  for (const f of forms) {
    if (!s.startsWith(f.pre) || !s.endsWith(f.post) || s.length < f.pre.length + f.post.length) continue
    const ids = s.slice(f.pre.length, s.length - f.post.length).split(',').map(x => f.labels[x.trim()])
    if (ids.length && ids.every(Boolean)) return ids
  }
  return null
}

// about_me with our line set to `line` (or removed when line is ''): the first
// line of ours is replaced in place, any other of ours dropped, the person's
// own lines kept. Without a line of ours, the new one goes at the end.
export function withHelpLine(forms, aboutMe, line) {
  const lines = aboutMe ? aboutMe.split('\n') : []
  const out = []
  let placed = false
  for (const l of lines) {
    if (!ourLine(forms, l)) { out.push(l); continue }
    if (!placed && line) out.push(line)
    placed = true
  }
  if (!placed && line) out.push(line)
  return out.join('\n')
}

function splitName(full) {
  const cut = full.lastIndexOf(' ')
  return cut > 0 ? [full.slice(0, cut), full.slice(cut + 1)] : [full, '']
}

// What the form starts from, read from the profile's Info entries.
function startFrom(forms, entries) {
  const v = {}
  for (const e of entries || []) if (e && !e.masked && typeof e.value === 'string') v[e.key] = e.value
  const help = new Set()
  for (const l of (v.about_me || '').split('\n')) for (const id of ourLine(forms, l) || []) help.add(id)
  return {
    name: tidy(v.full_name || [v.first_name, v.last_name].filter(Boolean).join(' ')),
    last: (v.last_name || '').trim(),
    role: (v.job_title || '').trim(),
    company: (v.employer || '').trim(),
    help: HELP.filter(id => help.has(id)),
    aboutMe: v.about_me || '',
  }
}

// The entries to write, in order: only fields that changed and are not empty;
// about_me also when the chips changed to none (our line is then removed).
export function infoEntries(t, forms, start, { name, role, company, help }) {
  const out = []
  const full = tidy(name)
  if (full && full !== start.name) {
    const [first, last] = splitName(full)
    out.push(['full_name', full], ['first_name', first])
    // A one-word name clears a stored last name (the one deliberate empty
    // write besides about_me), so the old one is not mixed with the new.
    if (last || start.last) out.push(['last_name', last])
  }
  if (role.trim() && role.trim() !== start.role) out.push(['job_title', role.trim()])
  if (company.trim() && company.trim() !== start.company) out.push(['employer', company.trim()])
  const chosen = HELP.filter(id => help.includes(id))
  if (chosen.join() !== start.help.join()) {
    const line = chosen.length ? t('onboarding.about.helpLine', { list: chosen.map(id => helpLabel(t, id)).join(', ') }) : ''
    const about = withHelpLine(forms, start.aboutMe, line)
    if (about !== start.aboutMe) out.push(['about_me', about])
  }
  return out
}

export default function AboutYou({ mark, setCanContinue, setOnContinue }) {
  const { t, i18n } = useTranslation()
  const [name, setName] = useState('')
  const [role, setRole] = useState('')
  const [company, setCompany] = useState('')
  const [help, setHelp] = useState([])
  const [loaded, setLoaded] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [error, setError] = useState('')
  const start = useRef(null)
  const alive = useRef(true)
  const forms = useRef(null)
  if (!forms.current) forms.current = helpLineForms(i18n)

  const load = useCallback(async () => {
    setLoadError('')
    const r = await callStep(() => GetProfileInfo())
    if (!alive.current) return
    if (!r.ok) { setLoadError(r.loginRequired ? t('onboarding.shell.signIn') : r.message); return }
    const s = startFrom(forms.current, r.value?.entries)
    start.current = s
    setName(s.name)
    setRole(s.role)
    setCompany(s.company)
    setHelp(s.help)
    setLoaded(true)
  }, [t])

  useEffect(() => {
    alive.current = true
    load()
    return () => { alive.current = false }
  }, [load])

  useEffect(() => { setCanContinue(loaded) }, [loaded, setCanContinue])

  useEffect(() => {
    setOnContinue(async () => {
      if (!start.current) return false
      setError('')
      const fail = (r) => { setError(r.loginRequired ? t('onboarding.shell.signIn') : r.message); return false }
      for (const [key, value] of infoEntries(t, forms.current, start.current, { name, role, company, help })) {
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
          <input id="onb-about-name" className="onb-input" type="text" autoComplete="name" disabled={!loaded} value={name} onChange={e => setName(e.target.value)} />
        </div>
        <div className="onb-field">
          <label htmlFor="onb-about-role">{t('onboarding.about.role')}</label>
          <input id="onb-about-role" className="onb-input" type="text" autoComplete="organization-title" disabled={!loaded} placeholder={t('onboarding.about.rolePlaceholder')} value={role} onChange={e => setRole(e.target.value)} />
        </div>
      </div>
      <div className="onb-field">
        <label htmlFor="onb-about-company">{t('onboarding.about.company')}</label>
        <input id="onb-about-company" className="onb-input" type="text" autoComplete="organization" disabled={!loaded} value={company} onChange={e => setCompany(e.target.value)} />
      </div>
      <div className="onb-field" style={{ margin: 0 }}>
        <span className="onb-label" id="onb-about-help">{t('onboarding.about.helpLabel')}</span>
        <div className="onb-chips" role="group" aria-labelledby="onb-about-help">
          {HELP.map(id => (
            <button key={id} type="button" className="onb-chip" disabled={!loaded} aria-pressed={help.includes(id)} onClick={() => toggle(id)}>
              {helpLabel(t, id)}
            </button>
          ))}
        </div>
      </div>
      {loadError && (
        <>
          <p className="onb-error" role="alert">{loadError}</p>
          <button type="button" className="onb-btn sm" style={{ marginTop: 10 }} onClick={load}>{t('onboarding.about.retry')}</button>
        </>
      )}
      {error && <p className="onb-error" role="alert">{error}</p>}
      <p className="onb-pointer"><PinIcon /><span>{t('onboarding.about.pointer')}</span></p>
    </div>
  )
}

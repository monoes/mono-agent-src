import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { UnsandboxedWarning } from '../components/chat/ChatWarnings.jsx'
import { useTranslation } from 'react-i18next'
import { Sparkles, ChevronDown, ChevronRight, Lock, AlertTriangle, CheckCircle2 } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { useReloadOnActivate } from '../lib/useReloadOnActivate'
import InfoField from '../components/info/InfoField.jsx'
import SendToCoderButton from '../components/SendToCoderButton.jsx'
import { groupCatalog, entryMap, hasValue, leftEmpty, errorText, profileType, TYPE_KEY } from '../components/info/infoModel.js'
import '../components/info/info.css'
import i18nOr from '../lib/i18nOr.js'

export default function Info({ isActive = true }) {
  const { t } = useTranslation()
  const [catalog, setCatalog] = useState(null)
  const [entries, setEntries] = useState([])
  const [loadError, setLoadError] = useState('')
  const [collapsed, setCollapsed] = useState(() => new Set())
  const [aiBusy, setAiBusy] = useState(false)
  const [aiResult, setAiResult] = useState(null) // { ok, text }
  const busyRef = useRef(false)

  const loadEntries = useCallback(async () => {
    const info = await WailsApp.GetProfileInfo()
    setEntries(info?.entries || [])
    return info?.entries || []
  }, [])

  const load = useCallback(async () => {
    try {
      const [cat] = await Promise.all([WailsApp.GetProfileInfoFields(), loadEntries()])
      setCatalog(cat)
      setLoadError('')
    } catch (e) {
      setLoadError(t('info.loadFailed', { message: errorText(e) }))
    }
  }, [loadEntries, t])

  useEffect(() => { load() }, [load])
  useReloadOnActivate(isActive, () => { if (!busyRef.current) load() })

  const type = useMemo(() => profileType(entries), [entries])
  const groups = useMemo(() => groupCatalog(catalog, type), [catalog, type])
  const byKey = useMemo(() => entryMap(entries), [entries])
  const secretKeys = useMemo(() => new Set((catalog?.fields || []).filter(f => f.sensitive || f.card).map(f => f.key)), [catalog])

  // Saves one field. A user edit makes the value the user's. A secret's
  // masked form comes from the CLI, so those re-read instead of echoing.
  const save = useCallback(async (key, value) => {
    await WailsApp.SetProfileInfo(key, value)
    if (secretKeys.has(key)) { await loadEntries(); return }
    setEntries(cur => {
      const rest = cur.filter(e => e.key !== key)
      return value === '' ? rest : [...rest, { key, value, masked: false, source: 'user', origin: '', updated_at: new Date().toISOString() }]
    })
  }, [secretKeys, loadEntries])

  const switchType = async (next) => {
    if (next === type) return
    setLoadError('')
    try {
      await WailsApp.SetProfileInfo(TYPE_KEY, next === 'business' ? 'Business' : 'Person')
      await loadEntries()
    } catch (e) {
      setLoadError(t('info.typeFailed', { message: errorText(e) }))
    }
  }

  const runAIFill = async () => {
    busyRef.current = true
    setAiBusy(true); setAiResult(null)
    try {
      const res = await WailsApp.AIFillProfileInfo('', '')
      const n = res?.filled?.length || 0
      let fresh
      try {
        fresh = await loadEntries()
      } catch {
        // The fill itself worked: do not tell the user to run it again.
        setAiResult({ ok: true, unsandboxed: !!res?.sandbox_status, text: t('infoPage.filledReloadFailed', { count: n }) })
        return
      }
      const empty = leftEmpty(catalog, fresh, profileType(fresh))
      setAiResult({ ok: true, unsandboxed: !!res?.sandbox_status, text: n > 0 ? t('info.aiDone', { count: n, empty }) : t('info.aiNothing', { empty }) })
    } catch (e) {
      setAiResult({ ok: false, text: t('info.aiFailed', { message: errorText(e) }) })
    } finally {
      busyRef.current = false
      setAiBusy(false)
    }
  }

  const toggle = (id) => setCollapsed(cur => {
    const next = new Set(cur)
    if (next.has(id)) next.delete(id); else next.add(id)
    return next
  })

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
      <div className="page-header">
        <div className="page-header-left"><h2 className="page-title" style={{ margin: 0 }}>{t('info.title')}</h2></div>
        <div className="page-header-right">
          <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>{t('info.aiFillHint')}</span>
          <SendToCoderButton label={i18nOr('ui6PagesMisc.fill_with_coder')} goal="Fill in my profile information (the Info page) from what you can find about me in my documents and profile. Ask me about anything you are unsure of."
            origin={{ page: 'info', label: i18nOr('ui6PagesMisc.info_origin') }} />
          <button type="button" className="btn btn-primary" onClick={runAIFill} disabled={aiBusy || !catalog} aria-busy={aiBusy}>
            {aiBusy ? <div className="spinner" style={{ width: 13, height: 13, borderColor: 'rgba(4,6,10,0.25)', borderTopColor: 'var(--void)' }} /> : <Sparkles size={14} />}
            {aiBusy ? t('info.aiFilling') : t('info.aiFill')}
          </button>
        </div>
      </div>
      <div className="page-body">
        <div className="info-type" role="radiogroup" aria-label={t('info.typeLabel')}>
          {['person', 'business'].map(v => (
            <button
              key={v}
              type="button"
              role="radio"
              aria-checked={type === v}
              className={`info-type-btn ${type === v ? 'is-on' : ''}`}
              disabled={!catalog}
              onClick={() => switchType(v)}
            >
              {t(v === 'business' ? 'info.typeBusiness' : 'info.typePerson')}
            </button>
          ))}
        </div>
        <p className="info-lead">{t(type === 'business' ? 'info.subtitleBusiness' : 'info.subtitle')}</p>
        <div role="status" aria-live="polite">
          {aiResult && (
            <div className={`info-banner ${aiResult.ok ? 'is-ok' : 'is-error'}`}>
              {aiResult.ok ? <CheckCircle2 size={14} color="var(--green)" /> : <AlertTriangle size={14} color="var(--red)" />}
              <span>{aiResult.text}</span>
            </div>
          )}
        </div>
        {aiResult?.unsandboxed && <UnsandboxedWarning />}
        {loadError && <div className="info-banner is-error" role="alert"><AlertTriangle size={14} color="var(--red)" /><span>{loadError}</span><button type="button" className="btn btn-ghost btn-sm" onClick={load}>{t('nodeRunner.modals.retry')}</button></div>}
        {!catalog && !loadError && <div className="info-lead">{t('info.loading')}</div>}
        {catalog && groups.length === 0 && <div className="info-lead">{t('info.empty')}</div>}
        {groups.map(g => {
          const open = !collapsed.has(g.id)
          const filled = g.fields.filter(f => hasValue(byKey.get(f.key))).length
          const isCard = g.fields.some(f => f.card)
          const Chevron = open ? ChevronDown : ChevronRight
          return (
            <section key={g.id} className="info-section" aria-labelledby={`info-h-${g.id}`}>
              <h3 style={{ margin: 0 }}>
                <button type="button" className="info-section-head" id={`info-h-${g.id}`} aria-expanded={open} aria-controls={`info-p-${g.id}`} onClick={() => toggle(g.id)}>
                  <Chevron size={14} aria-hidden="true" />
                  {g.label}
                  <span className="info-section-count">{t('info.sectionCount', { filled, total: g.fields.length })}</span>
                </button>
              </h3>
              <div id={`info-p-${g.id}`} hidden={!open}>
                {isCard && <p className="info-card-note"><Lock size={11} style={{ verticalAlign: -1, marginRight: 5 }} />{t('info.cardNote')}</p>}
                <div className="info-grid">
                  {g.fields.map(f => (
                    <InfoField key={f.key} field={f} entry={byKey.get(f.key)} onSave={save} onChanged={loadEntries} />
                  ))}
                </div>
              </div>
            </section>
          )
        })}
      </div>
    </div>
  )
}

import { useState, useEffect, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { ShieldAlert, X } from 'lucide-react'
import { api } from '../../services/api.js'
import i18nOr from '../../lib/i18nOr.js'

// Agent browser permissions (plan C8): what AI agents may do in the
// browsers that have the MonoAgent Bridge extension, as a level per agent
// kind (coder chats, org roles, chat and workflow runs, other agents) plus
// site rules that allow or block a site for all of them. Machine-wide, not a
// profile's. A person using a terminal is never restricted. Everything goes
// through `monoagentcli browser permissions` (app_browser_permissions.go);
// the levels, their labels and descriptions are the CLI's catalog.

const mono = 'var(--font-mono)'
const card = {
  background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)',
  padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 16,
}
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }
const riskText = { display: 'flex', alignItems: 'flex-start', gap: 6, fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, marginTop: 4 }
const row = { display: 'flex', alignItems: 'center', gap: 12, borderTop: '1px solid var(--border)', paddingTop: 10 }
const name = { fontFamily: 'var(--font-body)', fontSize: 12.5, color: 'var(--text)' }
const tag = (color) => ({ fontFamily: mono, fontSize: 9, color, border: `1px solid ${color}`, borderRadius: 4, padding: '0 5px', marginLeft: 8, letterSpacing: 0.5, opacity: 0.85 })

// The riskiest level: any site, and form submits go through unasked.
export const RISKIEST_LEVEL = 'full-no-approvals'

const errMsg = (e) => String(e?.message || e || i18nOr('ui6SetCoder.unknown_error'))

export default function BrowserPermissionsSection() {
  const { t } = useTranslation()
  const [perm, setPerm] = useState(null)
  const [loadErr, setLoadErr] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [origin, setOrigin] = useState('')
  const [rule, setRule] = useState('allow')

  const load = useCallback(async () => {
    try { setPerm(await api.browserPermissions()); setLoadErr('') } catch (e) { setLoadErr(errMsg(e)) }
  }, [])
  useEffect(() => { load() }, [load])

  // run makes one change through the CLI, then reads the settings back.
  const run = async (what, fn) => {
    setBusy(what); setErr('')
    try { await fn(); await load(); return true } catch (e) { setErr(errMsg(e)); return false } finally { setBusy('') }
  }

  const addRule = async (e) => {
    e.preventDefault()
    const o = origin.trim()
    if (!o) return
    if (await run('site', () => api.setBrowserSiteRule(o, rule))) setOrigin('')
  }

  if (!perm) {
    return (
      <div style={card} data-testid="browser-permissions">
        {loadErr ? <div style={errText} role="alert">{loadErr}</div> : <div style={hint}>{t('ui6Set.loading')}</div>}
      </div>
    )
  }
  const catalog = Array.isArray(perm.catalog) ? perm.catalog : []
  const kinds = Array.isArray(perm.kinds) ? perm.kinds : []
  const sites = Array.isArray(perm.sites) ? perm.sites : []
  const levelOf = (id) => catalog.find(c => c.id === id)

  return (
    <div style={card} data-testid="browser-permissions">
      <div style={label}>{t('ui6SetBrowser.perm_title')}</div>
      <div style={hint}>
        {t('ui6SetBrowser.perm_about')}
      </div>
      <div style={hint} data-testid="kind-token-note">
        {t('ui6SetBrowser.perm_token')}
      </div>
      {err && <div style={errText} role="alert">{err}</div>}

      {kinds.map(k => {
        const level = levelOf(k.level)
        return (
          <div key={k.kind} data-kind={k.kind} style={{ ...row, alignItems: 'flex-start' }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={name}>
                {k.label}
                {k.isDefault && <span style={tag('var(--text-muted)')}>{t('ui6SetBrowser.default_tag')}</span>}
              </div>
              <div style={hint}>{level ? level.description : k.level}</div>
              {k.level === RISKIEST_LEVEL && (
                <div style={riskText} data-testid="permission-risk">
                  <ShieldAlert size={12} style={{ flexShrink: 0, marginTop: 1 }} />
                  <span>{t('ui6SetBrowser.perm_risk')}</span>
                </div>
              )}
            </div>
            <select
              aria-label={t('ui6SetBrowser.perm_for', { kind: k.label })}
              value={k.level}
              disabled={!!busy}
              onChange={e => run(`kind:${k.kind}`, () => api.setBrowserPermission(k.kind, e.target.value))}
              style={{ minWidth: 230 }}
            >
              {catalog.map(c => (
                <option key={c.id} value={c.id} title={c.description}>{c.id === RISKIEST_LEVEL ? '⚠ ' : ''}{c.label}</option>
              ))}
              {!level && <option value={k.level}>{k.level}</option>}
            </select>
          </div>
        )
      })}

      <div style={{ ...label, marginTop: 6 }}>{t('ui6SetBrowser.site_rules')}</div>
      <div style={hint} data-testid="site-rules-note">
        {t('ui6SetBrowser.site_rules_note', { dev: levelOf('dev')?.label || 'dev', ask: levelOf('ask')?.label || 'ask' })}
      </div>
      {sites.length === 0 && <div style={hint}>{t('ui6SetBrowser.no_rules')}</div>}
      {sites.map(s => (
        <div key={s.origin} data-origin={s.origin} style={row}>
          <span style={{ ...name, fontFamily: mono, fontSize: 11.5, flex: 1, minWidth: 0, wordBreak: 'break-all' }}>{s.origin}</span>
          <span style={tag(s.rule === 'block' ? 'var(--red)' : 'var(--green-neon)')}>{s.rule === 'block' ? t('ui6SetBrowser.block') : t('ui6SetBrowser.allow')}</span>
          <button type="button" className="btn btn-ghost btn-sm" disabled={!!busy}
            aria-label={t('ui6SetBrowser.remove_rule_for', { origin: s.origin })} title={t('ui6SetBrowser.remove_rule')}
            onClick={() => run('site', () => api.setBrowserSiteRule(s.origin, 'clear'))}>
            <X size={12} />
          </button>
        </div>
      ))}
      <form onSubmit={addRule} style={{ ...row, flexWrap: 'wrap' }}>
        <input className="form-input" aria-label={t('ui6SetBrowser.site')} placeholder="https://example.com" spellCheck={false}
          value={origin} onChange={e => setOrigin(e.target.value)} disabled={!!busy}
          style={{ flex: 1, minWidth: 180, fontFamily: mono, fontSize: 12 }} />
        <select aria-label={t('ui6SetBrowser.site_rule')} value={rule} onChange={e => setRule(e.target.value)} disabled={!!busy} style={{ minWidth: 90 }}>
          <option value="allow">{t('ui6SetBrowser.allow')}</option>
          <option value="block">{t('ui6SetBrowser.block')}</option>
        </select>
        <button type="submit" className="btn btn-primary btn-sm" disabled={!!busy || !origin.trim()}>{t('ui6SetBrowser.add_rule')}</button>
      </form>
    </div>
  )
}

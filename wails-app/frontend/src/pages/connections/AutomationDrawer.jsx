// Automation detail drawer (spec §7.2): tabs Overview / Session / Actions /
// Health / Recordings, and a footer with the package lifecycle commands.
// Everything shown comes from `automation show <id> --json` and friends.
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '../../i18n.js'
import { X, Download, RotateCcw, Power, Trash2, Undo2, UploadCloud } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { confirm } from '../../components/ConfirmDialog.jsx'
import { Chip, ErrorBox, OkBox, Busy, SOURCE_LABELS, trustName, mono, muted, body, useDialog } from './ui.jsx'
import OverviewTab from './OverviewTab.jsx'
import SessionTab from './SessionTab.jsx'
import ActionsTab from './ActionsTab.jsx'
import HealthTab from './HealthTab.jsx'
import RecordingsTab from './RecordingsTab.jsx'
import PublishToMonoesDialog from '../../components/library/PublishToMonoesDialog.jsx'
import { library } from '../../services/library.js'

const TABS = ['Overview', 'Session', 'Actions', 'Health', 'Recordings']

// isOlder reports whether version a sorts before b (numeric dot parts), so
// the footer says "Roll back" for an older previous version and "Switch"
// after a rollback, when the kept previous version is the newer one.
function isOlder(a, b) {
  const pa = String(a || '').split(/[.+-]/).map(Number), pb = String(b || '').split(/[.+-]/).map(Number)
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] || 0, y = pb[i] || 0
    if (Number.isNaN(x) || Number.isNaN(y)) return false
    if (x !== y) return x < y
  }
  return false
}

// Tabs follows the ARIA tabs pattern: arrow keys, Home and End move between
// tabs; only the selected tab is in the Tab order.
function Tabs({ tabs, current, onSelect }) {
  const { t: tr } = useTranslation()
  const onKeyDown = (e) => {
    const i = tabs.indexOf(current)
    const next = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key]
    if (next === undefined) return
    e.preventDefault()
    const t = tabs[(next + tabs.length) % tabs.length]
    onSelect(t)
    document.getElementById(`automation-tab-${t}`)?.focus()
  }
  return (
    <div role="tablist" aria-label={tr('ui6ConnAuto.sections')} onKeyDown={onKeyDown} style={{ display: 'flex', gap: 2, marginTop: 14 }}>
      {tabs.map(t => (
        <button
          key={t}
          id={`automation-tab-${t}`}
          role="tab"
          aria-selected={current === t}
          aria-controls="automation-tabpanel"
          tabIndex={current === t ? 0 : -1}
          onClick={() => onSelect(t)}
          style={{ ...mono, fontSize: 11, padding: '7px 12px', background: 'none', border: 'none', cursor: 'pointer', color: current === t ? 'var(--cyan-bright)' : 'var(--text-muted)', borderBottom: `2px solid ${current === t ? 'var(--cyan)' : 'transparent'}`, marginBottom: -1 }}
        >
          {tr(`ui6ConnAuto.tab_${t}`)}
        </button>
      ))}
    </div>
  )
}

export default function AutomationDrawer({ automation, initialTab = 'Overview', onClose, onChanged }) {
  const id = automation.id
  // An uninstalled built-in has no package to show (`automation show`
  // refuses it): the drawer only offers to reinstall it from monoes.me.
  const removed = !!automation.removed
  const tabs = removed ? ['Overview'] : TABS
  const dialog = useDialog(onClose)
  const [tab, setTab] = useState(initialTab)
  const [detail, setDetail] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [note, setNote] = useState(null) // footer result {ok, text}
  const [publishing, setPublishing] = useState(false)
  const { t } = useTranslation()

  const load = useCallback(async () => {
    setError('')
    if (removed) { setDetail(null); return }
    const res = await api.showAutomation(id)
    if (!res || res.error) { setError(res?.error || i18n.t('ui6ConnAuto.load_failed')); return }
    setDetail(res)
  }, [id, removed])

  useEffect(() => { load() }, [load])

  const info = { ...automation, ...(detail?.info || {}) }

  const lifecycle = async (verb, fn, question) => {
    if (question && !(await confirm(question))) return
    setBusy(verb)
    try {
      const res = await fn(id)
      if (!res || res.error) { notify(t(`ui6ConnAuto.op_${verb}`), res?.error || t(`ui6ConnAuto.failed_${verb}`)); return }
      await Promise.all([load(), onChanged?.()])
      if (verb === 'uninstall') onClose()
    } finally { setBusy('') }
  }

  const exportPackage = async () => {
    const path = await api.chooseAutomationExportPath(`${id}-${info.version || 'latest'}.mpkg`)
    if (!path) return
    setBusy('export')
    try {
      const res = await api.exportAutomation(id, path)
      setNote(!res || res.error ? { ok: false, text: res?.error || t('ui6ConnAuto.export_failed') } : { ok: true, text: t('ui6ConnAuto.exported_to', { path: res.file || path }) })
    } finally { setBusy('') }
  }

  return (
    <div onClick={e => e.target === e.currentTarget && onClose()} style={{ position: 'fixed', inset: 0, background: 'rgba(4,6,10,.6)', zIndex: 1000, display: 'flex', justifyContent: 'flex-end' }}>
      <aside
        {...dialog}
        role="dialog"
        aria-modal="true"
        aria-labelledby="automation-drawer-title"
        style={{ width: 'min(620px, 100vw)', height: '100%', background: 'var(--elevated)', borderLeft: '1px solid var(--border-bright)', boxShadow: 'var(--shadow-glow)', display: 'flex', flexDirection: 'column', animation: 'slide-up 180ms ease' }}
      >
        <header style={{ padding: '16px 20px 0', borderBottom: '1px solid var(--border)' }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div id="automation-drawer-title" style={{ fontFamily: 'var(--font-display)', fontSize: 17, fontWeight: 700, color: 'var(--text)' }}>{info.name || id}</div>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 5, marginTop: 6, alignItems: 'center' }}>
                <span style={{ ...muted, fontSize: 10 }}>{id}</span>
                <Chip color="var(--cyan)">{SOURCE_LABELS[info.source] || info.source} {info.version}</Chip>
                {info.trust && info.trust !== info.source && <Chip>{t('ui6ConnAuto.trust_chip', { trust: trustName(info.trust) })}</Chip>}
                {!info.removed && info.available === false && <Chip color="var(--red)">{t('ui6ConnAuto.unavailable')}</Chip>}
                {info.available !== false && info.enabled === false && <Chip>{t('ui6ConnAuto.disabled')}</Chip>}
                {info.modified && <Chip color="var(--yellow)">{t('ui6ConnAuto.modified')}</Chip>}
                {info.containsScripts && <Chip color="var(--orange)">{t('ui6ConnAuto.contains_scripts')}</Chip>}
                {info.pendingUpdate && <Chip color="var(--green-neon)">{t('ui6ConnAuto.update_held', { version: info.pendingUpdate })}</Chip>}
              </div>
            </div>
            <button className="btn btn-ghost btn-icon" onClick={onClose} aria-label={t('ui6Conn.close')}><X size={16} /></button>
          </div>
          <Tabs tabs={tabs} current={tabs.includes(tab) ? tab : 'Overview'} onSelect={setTab} />
        </header>

        <div role="tabpanel" id="automation-tabpanel" aria-labelledby={`automation-tab-${tab}`} style={{ flex: 1, overflowY: 'auto', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12 }}>
          <ErrorBox>{error}</ErrorBox>
          {removed && <div style={body}>{t('library.reinstallNote')}</div>}
          {!removed && !detail && !error && <Busy text={t('ui6Conn.loading')} />}
          {detail && tab === 'Overview' && <OverviewTab info={info} manifest={detail.manifest || {}} issues={detail.issues || []} fragments={detail.fragments || []} onTrustChanged={() => Promise.all([load(), onChanged?.()])} />}
          {!removed && tab === 'Session' && <SessionTab automation={info} manifest={detail?.manifest} onChanged={onChanged} />}
          {detail && tab === 'Actions' && <ActionsTab automationId={id} actions={detail.actions || []} />}
          {!removed && tab === 'Health' && <HealthTab automationId={id} />}
          {!removed && tab === 'Recordings' && <RecordingsTab automationId={id} onSaved={() => Promise.all([load(), onChanged?.()])} />}
        </div>

        {note && (
          <div style={{ padding: '0 20px 10px' }}>
            {note.ok ? <OkBox>{note.text}</OkBox> : <ErrorBox>{note.text}</ErrorBox>}
          </div>
        )}
        <footer style={{ padding: '12px 20px', borderTop: '1px solid var(--border)', display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          <button className="btn btn-secondary btn-sm" onClick={exportPackage} disabled={!!busy || removed} style={{ gap: 5 }}>
            <Download size={11} /> {busy === 'export' ? t('ui6ConnAuto.exporting') : t('ui6ConnAuto.export_package')}
          </button>
          <button className="btn btn-secondary btn-sm" onClick={() => setPublishing(true)} disabled={!!busy || removed} style={{ gap: 5 }}>
            <UploadCloud size={11} /> {t('library.publish.button')}
          </button>
          {!removed && info.previousVersion && (
            <button className="btn btn-ghost btn-sm" disabled={!!busy} style={{ gap: 5 }}
              onClick={() => lifecycle('rollback', api.rollbackAutomation, t('ui6ConnAuto.switch_confirm', { name: info.name || id, from: info.version, to: info.previousVersion }))}>
              <RotateCcw size={11} /> {isOlder(info.previousVersion, info.version) ? t('ui6ConnAuto.roll_back_to', { version: info.previousVersion }) : t('ui6ConnAuto.switch_back_to', { version: info.previousVersion })}
            </button>
          )}
          {info.removed ? (
            <button className="btn btn-primary btn-sm" disabled={!!busy} style={{ gap: 5 }}
              onClick={() => lifecycle('reinstall', (pkg) => library.install('automation', pkg, { yes: true }))}>
              <Undo2 size={11} /> {busy === 'reinstall' ? t('library.adding') : t('library.reinstall')}
            </button>
          ) : info.enabled === false ? (
            <button className="btn btn-ghost btn-sm" disabled={!!busy || info.available === false} title={info.unavailableReason || ''} style={{ gap: 5 }} onClick={() => lifecycle('enable', api.enableAutomation)}>
              <Power size={11} /> {t('ui6ConnAuto.enable')}
            </button>
          ) : (
            <button className="btn btn-ghost btn-sm" disabled={!!busy} style={{ gap: 5 }} onClick={() => lifecycle('disable', api.disableAutomation)}>
              <Power size={11} /> {t('ui6ConnAuto.disable')}
            </button>
          )}
          <div style={{ flex: 1 }} />
          {!info.removed && (
            <button className="btn btn-danger btn-sm" disabled={!!busy} style={{ gap: 5 }}
              onClick={() => lifecycle('uninstall', api.uninstallAutomation, info.source === 'builtin' ? t('ui6ConnAuto.uninstall_confirm_builtin', { name: info.name || id }) : t('ui6ConnAuto.uninstall_confirm', { name: info.name || id }))}>
              <Trash2 size={11} /> {t('ui6ConnAuto.uninstall')}
            </button>
          )}
        </footer>
      </aside>
      {publishing && (
        <PublishToMonoesDialog kind="automation" localId={id} defaultName={info.name || id}
          defaultDescription={info.description || ''} onClose={() => setPublishing(false)} />
      )}
    </div>
  )
}

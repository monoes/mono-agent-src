// Pieces of the workflow import dialog that render the CLI's report on
// bundled automations: per-package status, the review of what would be
// installed, packages the file does not carry, and packages whose bundled
// copy differs from the installed one.
import { useTranslation } from 'react-i18next'
import { Package } from 'lucide-react'
import { Chip, body, label, mono, muted, panel, trustName } from '../connections/ui.jsx'
import i18nOr from '../../lib/i18nOr.js' // for messages built outside components

export const STATUS_TEXT = {
  created: (name) => i18nOr('ui6ConnWf.imported', { name }),
  updated: (name) => i18nOr('ui6ConnWf.updated', { name }),
  unchanged: () => i18nOr('ui6ConnWf.unchanged'),
}
const ITEM_COLORS = { present: 'var(--green-neon)', installed: 'var(--green-neon)', replaced: 'var(--green-neon)', missing: 'var(--yellow)', differs: 'var(--yellow)', conflict: 'var(--red)', failed: 'var(--red)' }

// installSummary describes the --yes re-run: the workflow itself comes back
// unchanged, so the news is what happened to the bundled packages.
export function installSummary(r) {
  const items = r.automations || []
  const done = items.filter(i => i.status === 'installed').length
  const replaced = items.filter(i => i.status === 'replaced').length
  const failed = items.filter(i => i.status === 'failed').length
  const parts = []
  if (done || !replaced) parts.push(i18nOr('ui6ConnWf.installed_count', { count: done, name: r.name || r.id }))
  if (replaced) parts.push(i18nOr('ui6ConnWf.replaced_count', { count: replaced }))
  if (failed) parts.push(i18nOr('ui6ConnWf.failed_count', { count: failed }))
  return parts.join(' ')
}

// ReviewDetail: what installing one bundled package means, from the CLI's
// dry-run review (reviewDetail), before anything is installed.
export function ReviewDetail({ item }) {
  const { t } = useTranslation()
  const d = item.reviewDetail
  const line = { fontFamily: 'var(--font-mono)', fontSize: 10.5 }
  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '6px 8px', display: 'flex', flexDirection: 'column', gap: 3 }}>
      <span style={{ ...line, fontSize: 11.5, color: 'var(--text)' }}>{item.id} {item.version}</span>
      {d ? (
        <>
          <span style={line}>{t('ui6ConnWf.publisher_line', { publisher: d.publisher || t('ui6ConnWf.unknown') })}</span>
          <span style={line}>{t('ui6ConnWf.domains_line', { domains: (d.domains || []).join(', ') || t('ui6ConnWf.unrestricted') })}</span>
          {/* The capabilities list already carries the visibility lines
              ("visits profiles — the profile's owner can see your visit…"),
              as in the automation import review. */}
          {(d.capabilities || []).length > 0 && (
            <ul style={{ margin: 0, paddingLeft: 16 }}>{d.capabilities.map(c => <li key={c} style={line}>{c}</li>)}</ul>
          )}
          {d.replaceRequired && d.replaces && <span style={{ ...line, color: 'var(--red)' }}>{t('ui6ConnWf.replaces', { source: trustName(d.replaces.source), id: d.replaces.id, version: d.replaces.version })}</span>}
          {d.trustChange && <span style={{ ...line, color: 'var(--yellow)' }}>{t('ui6ConnWf.trust_drop', { from: trustName(d.trustChange.from), to: trustName(d.trustChange.to) })}</span>}
        </>
      ) : item.review ? <span style={line}>{item.review}</span> : null}
      {item.error && <span style={{ ...line, color: 'var(--red)' }}>{item.error}</span>}
    </div>
  )
}

// ReviewNotes: what the one-line review leaves out — the replacement, a
// trust drop, and the plain-language capabilities (visibility included);
// the technical ones (steps, scripts, tier…) are already in the review line.
const TECHNICAL_CAP = /^(steps|scripts|tier):|^(downloads|policy-blocked)$/
function ReviewNotes({ d }) {
  const { t } = useTranslation()
  if (!d) return null
  const note = { ...muted, fontSize: 10, paddingLeft: 19, wordBreak: 'break-word' }
  return (
    <>
      {d.replaceRequired && d.replaces && <span style={{ ...note, color: 'var(--red)' }}>{t('ui6ConnWf.replaces', { source: trustName(d.replaces.source), id: d.replaces.id, version: d.replaces.version })}</span>}
      {d.trustChange && <span style={{ ...note, color: 'var(--yellow)' }}>{t('ui6ConnWf.trust_drop', { from: trustName(d.trustChange.from), to: trustName(d.trustChange.to) })}</span>}
      {(d.capabilities || []).filter(c => !TECHNICAL_CAP.test(c)).map(c => <span key={c} style={note}>• {c}</span>)}
    </>
  )
}

// copyOfExisting returns the id of the workflow an import was kept apart
// from: the CLI's copyOf, else (older CLIs) the id in its "imported as a
// copy" warning. Every copy reason's warning ends "…: <id>; imported as a
// copy — use --replace …"; the dialog's notice replaces it.
export const COPY_WARNING = /:\s*([^\s;]+);\s*imported as a copy/i
export function copyOfExisting(res) {
  if (res?.copyOf) return res.copyOf
  for (const w of res?.warnings || []) {
    const m = COPY_WARNING.exec(w)
    if (m) return m[1]
  }
  return null
}

// splitBundle separates packages the file carries (installable when
// missing) from ones the exporter could not include (notBundled).
// copyReasonOf is why the import became a copy: "name" (a same-named
// workflow) or "edited" (the file's own id, or an earlier import of this
// file, was edited here — CLI reasons "id" and "import"). Older CLIs send
// no reason; their warning is about a same-named workflow.
export function copyReasonOf(res) {
  const r = res?.copyReason
  if (r === 'id' || r === 'import') return 'edited'
  return 'name'
}

export function splitBundle(items) {
  const all = items || []
  const notIncluded = all.filter(i => i.notBundled && i.status === 'missing')
  const differs = all.filter(i => i.status === 'differs')
  const listed = all.filter(i => !notIncluded.includes(i) && !differs.includes(i))
  const installable = listed.filter(i => i.status === 'missing')
  const replaceable = differs.filter(isReplaceable)
  return { listed, installable, notIncluded, differs, replaceable }
}

// isBuiltinCopy: the installed copy a "differs" package would replace is a
// built-in, which a workflow file never replaces. The CLI's builtin flag,
// else (older CLIs) the review's replaces source.
export function isBuiltinCopy(item) {
  if (item?.builtin) return true
  const r = item?.reviewDetail?.replaces
  return r?.source === 'builtin' || r?.trust === 'builtin'
}

// isReplaceable: a "differs" package the file's version may replace — the
// CLI's replaceable, else anything that is not a built-in.
export function isReplaceable(item) {
  if (typeof item?.replaceable === 'boolean') return item.replaceable
  return !isBuiltinCopy(item)
}

// notBundledReason is the exporter's reason alone: the CLI's "not in the
// bundle:" prefix and its "; the sender can …" advice (which names CLI
// flags) are dropped; the dialog words the advice itself.
export function notBundledReason(item) {
  return (item?.error || '').replace(/^not in the bundle:\s*/i, '').replace(/;\s*the sender can [\s\S]*$/i, '')
}

export function NotIncluded({ items }) {
  const { t } = useTranslation()
  if (!items.length) return null
  return (
    <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 6 }}>
      <span style={label}>{t('ui6ConnWf.not_included')}</span>
      <span style={{ ...body, fontSize: 11 }}>
        {t('ui6ConnWf.not_carried', { count: items.length })}
      </span>
      {items.map(it => (
        <div key={it.id} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span style={{ ...mono, fontSize: 11, color: 'var(--text)' }}><Package size={11} color="var(--text-muted)" style={{ verticalAlign: -1, marginRight: 6 }} />{it.id} <span style={muted}>{it.version}</span></span>
          {it.localOnly
            ? <span style={{ ...muted, fontSize: 10, paddingLeft: 19 }}>{t('ui6ConnWf.local_only')}</span>
            : <>
                {notBundledReason(it) && <span style={{ ...muted, fontSize: 10, paddingLeft: 19, wordBreak: 'break-word' }}>{notBundledReason(it)}</span>}
                <span style={{ ...muted, fontSize: 10, paddingLeft: 19 }}>{t('ui6ConnWf.install_other_way')}</span>
              </>}
        </div>
      ))}
    </div>
  )
}

export function Automations({ items }) {
  const { t } = useTranslation()
  if (!items?.length) return null
  return (
    <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 6 }}>
      <span style={label}>{t('ui6ConnWf.bundled')}</span>
      {items.map(it => (
        <div key={it.id} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <Package size={11} color="var(--text-muted)" />
            <span style={{ ...mono, fontSize: 11, color: 'var(--text)', flex: 1 }}>{it.id} <span style={muted}>{it.version}</span></span>
            <Chip color={ITEM_COLORS[it.status] || 'var(--text-muted)'}>{t(`ui6ConnWf.item_value_${it.status}`, { defaultValue: it.status })}{it.installedVersion && it.status === 'present' ? ` ${it.installedVersion}` : ''}</Chip>
          </div>
          {it.error && <span style={{ ...muted, fontSize: 10, color: 'var(--red)', paddingLeft: 19 }}>{it.error}</span>}
          {it.review && <span style={{ ...muted, fontSize: 10, paddingLeft: 19, wordBreak: 'break-word' }}>{it.review}</span>}
          <ReviewNotes d={it.reviewDetail} />
        </div>
      ))}
    </div>
  )
}

// CHANGE_LABELS names each ReviewChanges field in plain words.
const CHANGE_LABELS = [
  ['addedDomains', 'ui6ConnWf.change_domains'],
  ['addedSteps', 'ui6ConnWf.change_steps'],
  ['addedCallActions', 'ui6ConnWf.change_actions'],
  ['addedScripts', 'ui6ConnWf.change_new_scripts'],
  ['changedScripts', 'ui6ConnWf.change_changed_scripts'],
]

export function ChangeList({ changes }) {
  const { t } = useTranslation()
  const rows = CHANGE_LABELS.filter(([k]) => (changes?.[k] || []).length)
  if (!rows.length) return <span style={{ ...muted, fontSize: 10 }}>{t('ui6ConnWf.same_permissions')}</span>
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      {rows.map(([k, key]) => (
        <span key={k} style={{ ...mono, fontSize: 10.5, color: 'var(--text-secondary)', wordBreak: 'break-word' }}>{t(key)}: {changes[k].join(', ')}</span>
      ))}
    </div>
  )
}

// Differs: installed packages whose bundled copy has the same version but
// other content. The installed copy is kept; replacing it is an explicit
// choice (the dialog's "Replace with the file's version").
export function Differs({ items }) {
  const { t } = useTranslation()
  if (!items.length) return null
  return (
    <div style={{ ...panel, borderColor: 'var(--yellow)', display: 'flex', flexDirection: 'column', gap: 8 }}>
      <span style={label}>{t('ui6ConnWf.differs_title')}</span>
      <span style={{ ...body, fontSize: 11 }}>
        {t('ui6ConnWf.differs_note', { count: items.length })}
      </span>
      {items.map(it => (
        <div key={it.id} style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
          <span style={{ ...mono, fontSize: 11, color: 'var(--text)' }}><Package size={11} color="var(--text-muted)" style={{ verticalAlign: -1, marginRight: 6 }} />{it.id} <span style={muted}>{it.version}</span></span>
          <div style={{ paddingLeft: 19 }}><ChangeList changes={it.changes} /></div>
          {/^(not replaced|could not compare)/i.test(it.error || '') && <span style={{ ...muted, fontSize: 10, paddingLeft: 19, color: 'var(--red)' }}>{it.error}</span>}
          {isBuiltinCopy(it)
            ? <span style={{ ...muted, fontSize: 10, paddingLeft: 19 }}>{t('ui6ConnWf.builtin_kept')}</span>
            : !isReplaceable(it) && <span style={{ ...muted, fontSize: 10, paddingLeft: 19 }}>{t('ui6ConnWf.cannot_replace')}</span>}
        </div>
      ))}
    </div>
  )
}

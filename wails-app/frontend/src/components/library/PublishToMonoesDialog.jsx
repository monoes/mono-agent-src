// "Publish to monoes": uploads a local workflow (id), web automation (id) or
// org (name) through `library publish`. Private unless ticked public. The
// CLI packs/exports the thing itself; this dialog only collects the fields.
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { X, UploadCloud, Loader, ExternalLink } from 'lucide-react'
import { library } from '../../services/library.js'
import { api } from '../../services/api.js'
import LogInToMonoesButton from './LogInToMonoesButton.jsx'
import { useEscapeClose } from '../../lib/escapeStack.js'

const mono = { fontFamily: 'var(--font-mono)', fontSize: 10.5 }

export default function PublishToMonoesDialog({ kind, localId, defaultName = '', defaultDescription = '', onClose }) {
  const { t } = useTranslation()
  const [status, setStatus] = useState(null)
  const [name, setName] = useState(defaultName)
  const [description, setDescription] = useState(defaultDescription)
  const [tags, setTags] = useState('')
  const [version, setVersion] = useState('')
  const [isPublic, setIsPublic] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [published, setPublished] = useState(null)

  useEffect(() => {
    let live = true
    library.status(false).then(s => { if (live) setStatus(s?.error ? { logged_in: false } : s) })
    return () => { live = false }
  }, [])

  useEscapeClose(() => { if (!busy) onClose?.() })

  const submit = async () => {
    setBusy(true); setError('')
    const res = await library.publish(kind, localId, { isPublic, name: name.trim(), description: description.trim(), tags, version: version.trim() })
    setBusy(false)
    if (res?.error) { setError(res.error); return }
    setPublished(res.item || res)
  }

  return (
    <div className="modal-overlay" style={{ zIndex: 1100 }} onMouseDown={e => { if (e.target === e.currentTarget && !busy) onClose?.() }}>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="publish-monoes-title" style={{ width: 460 }}>
        <div className="modal-title">
          <span id="publish-monoes-title">{t('library.publish.title')}</span>
          <button className="btn btn-ghost btn-icon" onClick={onClose} disabled={busy} aria-label={t('library.close')}><X size={15} /></button>
        </div>

        {!status ? (
          <div style={{ ...mono, color: 'var(--text-muted)' }}>{t('library.checking')}</div>
        ) : !status.logged_in ? (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div style={{ fontFamily: 'var(--font-body)', fontSize: 12, color: 'var(--text-secondary)' }}>{t('library.publish.loginFirst')}</div>
            <LogInToMonoesButton status={status} onStatusChange={setStatus} />
          </div>
        ) : published ? (
          <div role="status" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div style={{ ...mono, color: 'var(--green)' }}>{t('library.publish.done', { name: published.name || name, version: published.version || '' })}</div>
            {published.url && (
              <button className="btn btn-secondary btn-sm" style={{ gap: 5, alignSelf: 'flex-start' }} onClick={() => api.openURL(published.url)}>
                <ExternalLink size={11} /> {t('library.publish.open')}
              </button>
            )}
            {published.url && <code style={{ ...mono, color: 'var(--cyan)', wordBreak: 'break-all' }}>{published.url}</code>}
          </div>
        ) : (
          <>
            <div style={{ marginBottom: 12 }}><LogInToMonoesButton status={status} onStatusChange={setStatus} compact /></div>
            <div className="form-group">
              <label className="form-label" htmlFor="publish-name">{t('library.publish.name')}</label>
              <input id="publish-name" className="form-input" value={name} onChange={e => setName(e.target.value)} />
            </div>
            <div className="form-group">
              <label className="form-label" htmlFor="publish-description">{t('library.publish.description')}</label>
              <textarea id="publish-description" className="form-textarea" style={{ fontFamily: 'var(--font-body)', minHeight: 64 }} value={description} onChange={e => setDescription(e.target.value)} />
            </div>
            <div style={{ display: 'flex', gap: 10 }}>
              <div className="form-group" style={{ flex: 2 }}>
                <label className="form-label" htmlFor="publish-tags">{t('library.publish.tags')}</label>
                <input id="publish-tags" className="form-input" placeholder={t('library.publish.tagsHint')} value={tags} onChange={e => setTags(e.target.value)} />
              </div>
              <div className="form-group" style={{ flex: 1 }}>
                <label className="form-label" htmlFor="publish-version">{t('library.publish.version')}</label>
                <input id="publish-version" className="form-input" placeholder={t('library.publish.versionHint')} value={version} onChange={e => setVersion(e.target.value)} />
              </div>
            </div>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontFamily: 'var(--font-body)', fontSize: 12, color: 'var(--text-secondary)', cursor: 'pointer' }}>
              <input type="checkbox" checked={isPublic} onChange={e => setIsPublic(e.target.checked)} />
              {t('library.publish.public')}
            </label>
            {!isPublic && <div style={{ ...mono, color: 'var(--text-muted)', marginTop: 4 }}>{t('library.publish.privateNote')}</div>}
            {error && <div role="alert" style={{ ...mono, color: 'var(--red)', marginTop: 10 }}>{error}</div>}
          </>
        )}

        <div className="modal-actions">
          <button className="btn btn-ghost btn-sm" onClick={onClose} disabled={busy}>{published ? t('library.close') : t('library.cancel')}</button>
          {status?.logged_in && !published && (
            <button className="btn btn-primary btn-sm" style={{ gap: 5 }} onClick={submit} disabled={busy || !name.trim()}>
              {busy ? <><Loader size={11} style={{ animation: 'spin .7s linear infinite' }} /> {t('library.publish.publishing')}</> : <><UploadCloud size={11} /> {t('library.publish.submit')}</>}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

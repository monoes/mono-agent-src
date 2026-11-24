import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FolderOpen } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { iconUrl } from './orgdesigner/roleIcons.js'
import IconPickerModal from './orgdesigner/IconPickerModal.jsx'
import { useEscapeClose } from '../lib/escapeStack.js'

const CreateProfile = WailsApp.CreateProfile ?? (async () => {})
const ChooseProfileFolder = WailsApp.ChooseProfileFolder ?? (async () => '')
const ListMonomindProjects = WailsApp.ListMonomindProjects ?? (async () => [])

/**
 * NewProfileModal — centered replacement for the old inline "+ New profile"
 * expand-in-place row. Adds two things the inline form never had: an icon
 * picker (delegating to the shared IconPickerModal, the same one Org
 * Designer roles use — no second picker built for this) and a list of the
 * user's existing monomind projects to pick from; picking one sets both the
 * Name field and the folder this profile's data will live in (root_dir),
 * exactly like manually typing a name and choosing a folder would.
 *
 * @param {boolean} open
 * @param {() => void} onClose
 * @param {(profile: object) => void} onCreated Called with the created
 *   ProfileInfo once CreateProfile succeeds, right before onClose.
 */
export default function NewProfileModal({ open, onClose, onCreated }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [rootDir, setRootDir] = useState('')
  const [icon, setIcon] = useState('')
  const [iconPickerOpen, setIconPickerOpen] = useState(false)
  const [projects, setProjects] = useState([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const nameRef = useRef(null)

  useEffect(() => {
    if (!open) return
    setName('')
    setRootDir('')
    setIcon('')
    setError('')
    setBusy(false)
    ListMonomindProjects().then(list => setProjects(Array.isArray(list) ? list : [])).catch(() => setProjects([]))
    const focusTimer = setTimeout(() => nameRef.current?.focus(), 30)
    return () => clearTimeout(focusTimer)
  }, [open])

  useEscapeClose(() => { if (!iconPickerOpen) onClose?.() }, open)

  if (!open) return null

  const pickProject = (project) => {
    setName(project.name)
    setRootDir(project.path)
  }

  const chooseOtherFolder = async () => {
    try {
      const path = await ChooseProfileFolder()
      if (path) setRootDir(path)
    } catch (e) {
      setError(e?.message || 'Failed to choose folder')
    }
  }

  const handleCreate = async () => {
    const trimmed = name.trim()
    if (!trimmed || busy) return
    setBusy(true)
    setError('')
    try {
      const profile = await CreateProfile(trimmed, rootDir, icon)
      onCreated?.(profile)
      onClose?.()
    } catch (e) {
      setError(e?.message || 'Failed to create profile')
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget) onClose?.() }}>
        <div role="dialog" aria-modal="true" aria-label={t('newProfileModal.title')} className="modal" style={{ width: 460 }}>
          <div className="modal-title">{t('newProfileModal.title')}</div>

          <div style={{ display: 'flex', gap: 10, marginBottom: 14 }}>
            <button
              onClick={() => setIconPickerOpen(true)}
              title={t('newProfileModal.chooseIcon')}
              style={{
                padding: 0, border: '2px solid rgba(0,180,216,0.3)', borderRadius: '50%',
                cursor: 'pointer', background: 'transparent', flexShrink: 0, width: 44, height: 44,
              }}
            >
              <img src={iconUrl(icon || 'coder')} alt="" style={{ width: '100%', height: '100%', borderRadius: '50%', display: 'block' }} />
            </button>
            <input
              ref={nameRef}
              className="form-input"
              value={name}
              onChange={e => setName(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') handleCreate() }}
              placeholder={t('newProfileModal.namePlaceholder')}
              style={{ flex: 1 }}
            />
          </div>

          {projects.length > 0 && (
            <div style={{ marginBottom: 14 }}>
              <div className="form-label">{t('newProfileModal.suggestedProjects')}</div>
              <div style={{ maxHeight: 160, overflowY: 'auto', border: '1px solid var(--border-dim)', borderRadius: 6 }}>
                {projects.map(p => (
                  <div
                    key={p.path}
                    onClick={() => pickProject(p)}
                    style={{
                      padding: '7px 10px', cursor: 'pointer',
                      background: rootDir === p.path ? 'rgba(0,180,216,0.1)' : 'transparent',
                      borderBottom: '1px solid var(--border-dim)',
                    }}
                    onMouseEnter={e => { if (rootDir !== p.path) e.currentTarget.style.background = 'rgba(255,255,255,0.04)' }}
                    onMouseLeave={e => { if (rootDir !== p.path) e.currentTarget.style.background = 'transparent' }}
                  >
                    <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--text)' }}>{p.name}</div>
                    <div
                      title={p.path}
                      style={{
                        fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)',
                        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                      }}
                    >{p.path}</div>
                  </div>
                ))}
              </div>
            </div>
          )}

          <div style={{ marginBottom: 14 }}>
            <button
              onClick={chooseOtherFolder}
              className="btn btn-secondary btn-sm"
              style={{ display: 'flex', alignItems: 'center', gap: 6 }}
            >
              <FolderOpen size={12} /> {t('newProfileModal.chooseDifferentFolder')}
            </button>
            {rootDir && (
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 6 }}>
                <span style={{
                  flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap', fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text-muted)',
                }}>→ {rootDir}</span>
                <span
                  onClick={() => setRootDir('')}
                  title={t('newProfileModal.useDefaultLocation')}
                  style={{ flexShrink: 0, fontSize: 12, color: 'var(--text-muted)', cursor: 'pointer' }}
                >×</span>
              </div>
            )}
          </div>

          {error && (
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#ff6b6b', marginBottom: 10 }}>{error}</div>
          )}

          <div className="modal-actions">
            <button className="btn btn-ghost" onClick={onClose}>{t('newProfileModal.cancel')}</button>
            <button className="btn btn-primary" onClick={handleCreate} disabled={busy || !name.trim()}>
              {busy ? t('newProfileModal.creating') : t('newProfileModal.create')}
            </button>
          </div>
        </div>
      </div>

      <IconPickerModal
        open={iconPickerOpen}
        currentIconId={icon}
        onSelect={setIcon}
        onClose={() => setIconPickerOpen(false)}
      />
    </>
  )
}

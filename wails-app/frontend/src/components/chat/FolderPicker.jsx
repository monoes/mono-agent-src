import { useTranslation } from 'react-i18next'
import { FolderRoot, FolderOpen, History } from 'lucide-react'
import { folderName, coderRuntimes, runtimeReadiness, useActiveProfile } from './useCoderMode.js'
import { runtimeLabel } from '../../lib/runtimeLabels.js'
import i18nOr from '../../lib/i18nOr.js'

// FolderPicker is where a coding chat works: the active profile's folder
// (default), a folder picked with the native dialog, or a recent one, and
// which runtimes are ready. workspace is { kind: 'root' } or { kind:
// 'folder', path }. Shown by a new chat's setup.

const mono = 'var(--font-mono)'
const CYAN = '#00b4d8'

function optionStyle(active) {
  return {
    display: 'flex', alignItems: 'flex-start', gap: 7, width: '100%', textAlign: 'left',
    padding: '6px 8px', borderRadius: 6, cursor: 'pointer', font: 'inherit',
    background: active ? 'rgba(0,180,216,0.1)' : 'transparent',
    border: `1px solid ${active ? 'rgba(0,180,216,0.35)' : 'transparent'}`,
    color: '#e2e8f0',
  }
}

const optTitle = { fontFamily: mono, fontSize: 10.5, color: '#e2e8f0' }
const optHint = { fontFamily: mono, fontSize: 9, color: 'var(--text-muted)', wordBreak: 'break-all', marginTop: 1 }

export function FolderPicker({ status, workspace, onWorkspaceChange, recent = [], onPickFolder, runtimeId = '', disabled = false, profile: given = null }) {
  const { t } = useTranslation()
  // A parent that already loaded the profile passes it, so the name is there at once.
  const loaded = useActiveProfile(!given && !!status?.enabled)
  const profile = given || loaded
  const agent = runtimeId ? runtimeLabel(runtimeId) : i18nOr('ui7Chat.the_agent')
  const pickedPath = workspace?.kind === 'folder' ? workspace.path : ''
  const pickedIsRecent = recent.some(w => w.path === pickedPath)
  return (

    <div data-testid="folder-picker" role="radiogroup" aria-label={i18nOr('ui7Chat.coder_workspace')} style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
      <button type="button" role="radio" aria-checked={workspace?.kind !== 'folder'} disabled={disabled}
        onClick={() => onWorkspaceChange({ kind: 'root' })} style={optionStyle(workspace?.kind !== 'folder')}>
        <FolderRoot size={12} color={CYAN} style={{ marginTop: 1, flexShrink: 0 }} />
        <span style={{ minWidth: 0 }}>
          <div style={optTitle}>
            {profile ? t('coderFolder.profileDefault', { name: profile.name }) : t('coderFolder.profileDefaultUnnamed')}
          </div>
          <div style={optHint}>{profile?.root_dir || t('coderFolder.profileHint')}</div>
        </span>
      </button>
      <button type="button" role="radio" aria-checked={!!pickedPath && !pickedIsRecent} disabled={disabled}
        onClick={onPickFolder} style={optionStyle(!!pickedPath && !pickedIsRecent)}>
        <FolderOpen size={12} color={CYAN} style={{ marginTop: 1, flexShrink: 0 }} />
        <span style={{ minWidth: 0 }}>
          <div style={optTitle}>{t('coderFolder.chooseAnother')}</div>
          <div style={optHint}>{pickedPath && !pickedIsRecent ? pickedPath : t('coderFolder.chooseHint')}</div>
        </span>
      </button>
      {recent.length > 0 && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 5, margin: '4px 0 1px 8px', fontFamily: mono, fontSize: 8.5, color: 'var(--text-muted)', letterSpacing: 1.5, textTransform: 'uppercase' }}>
          <History size={9} /> {i18nOr('ui7Chat.recent')}
        </div>
      )}
      {recent.slice(0, 5).map(w => (
        <button key={w.path} type="button" role="radio" aria-checked={pickedPath === w.path} disabled={disabled}
          title={w.path} onClick={() => onWorkspaceChange({ kind: 'folder', path: w.path })} style={optionStyle(pickedPath === w.path)}>
          <span style={{ minWidth: 0, paddingLeft: 19 }}>
            <div style={optTitle}>{folderName(w.path)}</div>
            <div style={optHint}>{w.path}{w.conversations ? ` · ${i18nOr('ui7Chat.chats', { count: w.conversations })}` : ''}</div>
          </span>
        </button>
      ))}
      <div style={{ fontFamily: mono, fontSize: 9, color: 'var(--text-muted)', lineHeight: 1.5, marginTop: 2 }}>
        {i18nOr('ui7Chat.no_asking', { agent })}
      </div>
      {Array.isArray(status.runtimes) && (
        <div data-testid="coder-runtimes" style={{ fontFamily: mono, fontSize: 9, color: 'var(--text-muted)', lineHeight: 1.5 }}>
          {coderRuntimes(status).map((r, i) => (
            <span key={r.id} style={{ color: r.ready ? '#10b981' : undefined }}>
              {i > 0 && ' · '}{runtimeLabel(r.id)} {runtimeReadiness(r)}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

import { useTranslation } from 'react-i18next'

const mono = 'var(--font-mono)'
const note = { fontFamily: mono, fontSize: 9.5, color: 'var(--text-muted)', overflowWrap: 'anywhere' }

// ApprovalCommand is what an approval of a shell call would run: the command,
// the folder the org file gives the role, and a warning when only the start of
// the command is known. All three come from the CLI (`org approvals`, `org
// chat history`); a call with no command shows nothing.
export function ApprovalCommand({ command, truncated, cwd }) {
  const { t } = useTranslation()
  if (!command) return null
  return (
    <div data-testid="approval-command" style={{ display: 'flex', flexDirection: 'column', gap: 3, marginTop: 4 }}>
      <pre aria-label={t('ask.commandLabel')} tabIndex={0} style={{ margin: 0, maxHeight: 160, overflow: 'auto', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', fontFamily: mono, fontSize: 11, color: 'var(--text)', background: 'var(--elevated)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '6px 8px' }}>{command}</pre>
      {cwd && <div style={note}>{t('ask.commandFolder', { path: cwd })}</div>}
      {truncated && <div role="note" style={{ ...note, color: '#fbbf24' }}>{t('ask.commandCut')}</div>}
    </div>
  )
}

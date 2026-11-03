// UpdateNotice: what an app update says about the daemon, the extension bridge and the other CLIs
// (UpdateResult.notice), with the two ways forward. The update is installed either way; "Later" only
// keeps the app running on the old code until it is restarted.
export default function UpdateNotice({ notice, onRestart, onLater, style }) {
  const btn = {
    background: 'none', border: '1px solid var(--border)', color: 'var(--text)', borderRadius: 3,
    padding: '2px 10px', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 10,
  }
  return (
    <div role="alert" data-testid="update-notice" style={{
      fontFamily: 'var(--font-mono)', fontSize: 10, color: '#fbbf24', border: '1px solid #fbbf24',
      borderRadius: 4, padding: '8px 10px', background: 'var(--surface)', maxWidth: 520, ...style,
    }}>
      <div style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word', marginBottom: 8 }}>{notice}</div>
      <div style={{ display: 'flex', gap: 8 }}>
        <button data-testid="update-notice-restart" onClick={onRestart} style={btn}>Restart now</button>
        <button data-testid="update-notice-later" onClick={onLater} style={btn}>Later</button>
      </div>
    </div>
  )
}

import { Component } from 'react'
import i18n from 'i18next'

// Catches render-time exceptions so a single component fault doesn't white-screen
// the whole desktop app (which has no browser refresh affordance).
export default class ErrorBoundary extends Component {
  constructor(props) {
    super(props)
    this.state = { error: null, copied: false }
  }

  copyDetails = () => {
    const e = this.state.error
    const text = [e?.message || String(e), e?.stack].filter(Boolean).join('\n')
    Promise.resolve(globalThis.navigator?.clipboard?.writeText?.(text))
      .then(() => this.setState({ copied: true }))
      .catch(() => {})
  }

  static getDerivedStateFromError(error) {
    return { error }
  }

  componentDidCatch(error, info) {
    console.error('Render error caught by ErrorBoundary:', error, info)
  }

  render() {
    if (!this.state.error) return this.props.children
    if (this.props.fallback) return this.props.fallback(this.state.error)
    return (
      <div style={{
        display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
        height: '100vh', gap: 16, padding: 24, textAlign: 'center',
        fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)',
        background: 'var(--base, #080c14)',
      }}>
        <div style={{ fontSize: 15, fontWeight: 600, color: 'var(--text)' }}>{i18n.t('errorBoundary.title', { defaultValue: 'Something went wrong' })}</div>
        <div style={{ fontSize: 12, color: 'var(--text-muted)', maxWidth: 480, wordBreak: 'break-word' }}>
          {this.state.error?.message || String(this.state.error)}
        </div>
        <button
          onClick={() => this.setState({ error: null, copied: false })}
          style={{
            marginTop: 8, padding: '8px 18px', cursor: 'pointer',
            background: '#00b4d8', color: '#fff', border: 'none', borderRadius: 6,
            fontFamily: 'var(--font-mono)', fontSize: 12,
          }}
        >
          {i18n.t('errorBoundary.reload', { defaultValue: 'Reload view' })}
        </button>
        <button
          type="button"
          onClick={this.copyDetails}
          style={{
            padding: '6px 14px', cursor: 'pointer', background: 'transparent',
            color: 'var(--text-secondary)', border: '1px solid var(--border, rgba(255,255,255,0.15))', borderRadius: 6,
            fontFamily: 'var(--font-mono)', fontSize: 11,
          }}
        >
          {this.state.copied ? i18n.t('errorBoundary.copied', { defaultValue: 'Copied' }) : i18n.t('errorBoundary.copyDetails', { defaultValue: 'Copy details' })}
        </button>
      </div>
    )
  }
}

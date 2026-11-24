// The two notices the app wears while the machine's monoes.me sign-in still
// allows work (spec §6.5): GraceBanner when monoes.me cannot be reached and the
// saved sign-in carries on offline (dismissible, for that grace window; when this
// computer dropped its saved sign-in because a refresh may have been lost, A24, it
// says so and offers to sign in again), and WarnBanner before the enforcement
// date for a machine with no usable sign-in (with a way to sign in now). Keys are
// written out in full for the key scan.
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Clock, X, LogIn } from 'lucide-react'
import LogInToMonoesButton from '../library/LogInToMonoesButton.jsx'
import { account, onAccountLogin } from '../../services/account.js'
import { useEscapeClose } from '../../lib/escapeStack.js'

const bar = {
  display: 'flex', alignItems: 'center', gap: 10, flexShrink: 0, padding: '7px 16px',
  borderBottom: '1px solid var(--border)', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)',
}
const GRACE_WHY = {
  unreachable: 'account.grace.unreachable',
  server_error: 'account.grace.serverError',
  keyring_unavailable: 'account.grace.keyringUnavailable',
}
const SIGNED_OUT = { logged_in: false }

export function GraceBanner({ status, onSignedIn }) {
  const { t, i18n } = useTranslation()
  const [dismissedFor, setDismissedFor] = useState('')
  const [open, setOpen] = useState(false)
  if (!status.grace_until || dismissedFor === status.grace_until) return null

  const until = new Date(status.grace_until)
  const minutes = Math.max(1, Math.round((until.getTime() - Date.now()) / 60000))
  const left = minutes >= 60 ? t('account.time.hours', { count: Math.floor(minutes / 60) }) : t('account.time.minutes', { count: minutes })
  const when = until.toLocaleString(i18n.language, { dateStyle: 'medium', timeStyle: 'short' })
  // A computer that dropped its saved sign-in cannot renew it, and signing in again is the only fix.
  const dropped = status.reason === 'unconfirmed'
  return (
    <>
      <div role="status" style={{ ...bar, background: 'rgba(234, 179, 8, 0.08)' }}>
        <Clock size={13} aria-hidden="true" style={{ color: 'var(--yellow)', flexShrink: 0 }} />
        <span style={{ flex: 1 }}>
          {dropped
            ? t('account.grace.unconfirmed', { when, left })
            : <>{t(GRACE_WHY[status.reason] ?? 'account.grace.unreachable')} {t('account.grace.keepsWorking', { when, left })}</>}
        </span>
        {dropped && <button className="btn btn-primary btn-sm" onClick={() => setOpen(true)}>{t('account.warn.signIn')}</button>}
        <button className="btn btn-ghost btn-icon" aria-label={t('account.banner.dismiss')} onClick={() => setDismissedFor(status.grace_until)}>
          <X size={13} />
        </button>
      </div>
      {open && <SignInDialog onClose={() => setOpen(false)} onSignedIn={onSignedIn} />}
    </>
  )
}

export function SignInDialog({ onClose, onSignedIn }) {
  const { t } = useTranslation()
  useEscapeClose(onClose)
  return (
    <div className="modal-overlay" style={{ zIndex: 1100 }} onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="account-signin-title"
        style={{ width: 460, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 14, padding: 24, textAlign: 'center' }}>
        <div className="modal-title" style={{ marginBottom: 0, width: '100%' }}>
          <span id="account-signin-title">{t('account.gate.notLoggedIn.title')}</span>
          <button className="btn btn-ghost btn-icon" onClick={onClose} aria-label={t('account.banner.close')}><X size={15} /></button>
        </div>
        <LogInToMonoesButton service={account} onLogin={onAccountLogin} status={SIGNED_OUT} onStatusChange={onSignedIn} large />
      </div>
    </div>
  )
}

export function WarnBanner({ enforceFrom, onSignedIn }) {
  const { t, i18n } = useTranslation()
  const [open, setOpen] = useState(false)
  const date = new Date(enforceFrom).toLocaleDateString(i18n.language, { year: 'numeric', month: 'long', day: 'numeric' })
  return (
    <>
      <div role="status" style={{ ...bar, background: 'rgba(0, 180, 216, 0.06)' }}>
        <LogIn size={13} aria-hidden="true" style={{ color: 'var(--cyan)', flexShrink: 0 }} />
        <span style={{ flex: 1 }}>{t('account.warn.text', { date })}</span>
        <button className="btn btn-primary btn-sm" onClick={() => setOpen(true)}>{t('account.warn.signIn')}</button>
      </div>
      {open && <SignInDialog onClose={() => setOpen(false)} onSignedIn={onSignedIn} />}
    </>
  )
}

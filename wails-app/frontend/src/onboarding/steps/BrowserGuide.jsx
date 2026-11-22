import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ClipboardSetText } from '../../wailsjs/runtime/runtime'
import { Problem } from './StepProblem.jsx'

// The pieces of "Connect your browser" that only show things: the install
// instructions for the chosen browser with their illustration, the
// profile diagram and the Copy button. Browser.jsx owns the state and the
// binding calls. Keys are written out in full.

// copyText tries the web clipboard, then the Wails one (the WebView may not
// offer navigator.clipboard). Resolves true when either worked.
export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch { /* falls back to the Wails clipboard */ }
  try {
    return (await ClipboardSetText(text)) !== false
  } catch {
    return false
  }
}

const CopyIcon = () => (
  <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
    <rect x="9" y="9" width="12" height="12" rx="2" /><path d="M5 15V5a2 2 0 0 1 2-2h10" />
  </svg>
)

// CopyButton writes text to the clipboard. The label says Copied only when
// it worked; when no clipboard took it, a line says to copy it by hand (the
// text is selectable on screen).
export function CopyButton({ text, label }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(null)
  const copy = async () => setCopied(await copyText(text))
  return (
    <>
      <button type="button" className="onb-btn sm" aria-label={label} onClick={copy}>
        <CopyIcon />{copied ? t('onboarding.browser.install.copied') : t('onboarding.browser.install.copy')}
      </button>
      {copied === false && <span className="onb-error" role="alert">{t('onboarding.browser.install.copyFailed')}</span>}
    </>
  )
}

// InstallSteps: open the extensions page, turn on Developer mode, Load
// unpacked with the extension folder (GetExtensionInfo, or InstallExtension
// when it was missing or old). Reinstall unpacks it again on request.
// browser is a DetectChromiumBrowsers row, or { id: 'other', name }.
export function InstallSteps({ browser, info, installing, problem, onRetry, onReinstall, onLoaded }) {
  const { t } = useTranslation()
  const other = browser.id === 'other'
  const where = other
    ? t('onboarding.browser.install.devOther')
    : browser.devModeSide === 'left' ? t('onboarding.browser.install.devLeft') : t('onboarding.browser.install.devRight')
  return (
    <>
      <ol className="onb-browser-ol">
        <li>
          <b>{t('onboarding.browser.install.openTitle')}</b>
          {other ? (
            <span className="onb-muted">{t('onboarding.browser.install.otherAddress')}</span>
          ) : (
            <>
              <span className="onb-muted">{t('onboarding.browser.install.typeAddress', { browser: browser.name })}</span>
              <span className="onb-browser-copy">
                <code>{browser.extensionsUrl}</code>
                <CopyButton text={browser.extensionsUrl} label={t('onboarding.browser.install.copyAddress')} />
              </span>
            </>
          )}
        </li>
        <li>
          <b>{t('onboarding.browser.install.devTitle')}</b>
          <span className="onb-muted">{where}</span>
        </li>
        <li>
          <b>{t('onboarding.browser.install.loadTitle')}</b>
          {info?.path && (
            <span className="onb-browser-copy">
              <code>{info.path}</code>
              <CopyButton text={info.path} label={t('onboarding.browser.install.copyFolder')} />
            </span>
          )}
          {installing && (
            <span className="onb-browser-busy" role="status">
              <span className="onb-spinner" aria-hidden="true" />{t('onboarding.browser.install.preparing')}
            </span>
          )}
          <Problem r={problem} />
          {problem && !installing && (
            <span><button type="button" className="onb-btn sm" onClick={onRetry}>{t('onboarding.browser.install.retry')}</button></span>
          )}
          {info?.path && !installing && !problem && (
            <span className="onb-hint">
              {t('onboarding.browser.install.reinstallWhy')}{' '}
              <button type="button" className="onb-btn sm ghost" onClick={onReinstall}>{t('onboarding.browser.install.reinstall')}</button>
            </span>
          )}
        </li>
      </ol>
      <ExtensionsPagePicture browser={browser} />
      <button type="button" className="onb-btn" disabled={!info?.path || installing} onClick={onLoaded}>
        {t('onboarding.browser.install.loaded')}
      </button>
    </>
  )
}

const FONT = '-apple-system,Segoe UI,sans-serif'

// Marker n: a numbered amber circle where the control is.
const Mark = ({ n, x, y }) => (
  <g>
    <circle cx={x} cy={y} r="12" fill="var(--onb-accent)" />
    <circle cx={x} cy={y} r="19" fill="none" stroke="var(--onb-accent)" opacity=".5" />
    <text x={x} y={y + 4.5} fontFamily={FONT} fontSize="12" fontWeight="800" textAnchor="middle" fill="var(--onb-accent-ink)">{n}</text>
  </g>
)

// ExtensionsPagePicture: a drawing of the browser's extensions page with
// 1 the address bar, 2 the Developer mode switch (left sidebar or top
// right, from devModeSide) and 3 the Load unpacked button.
function ExtensionsPagePicture({ browser }) {
  const { t } = useTranslation()
  const left = browser.devModeSide === 'left'
  const label = left ? t('onboarding.browser.guide.labelLeft', { browser: browser.name }) : t('onboarding.browser.guide.labelRight', { browser: browser.name })
  const addr = browser.id === 'other' ? t('onboarding.browser.guide.addressOther') : browser.extensionsUrl
  const toggle = (x, y) => (
    <g><rect x={x} y={y} width="26" height="14" rx="7" fill="var(--onb-accent)" /><circle cx={x + 19} cy={y + 7} r="5" fill="oklch(0.96 0.01 290)" /></g>
  )
  const load = (x, y) => (
    <g>
      <rect x={x} y={y} width="104" height="24" rx="6" fill="oklch(0.83 0.15 72 / .2)" stroke="var(--onb-accent)" />
      <text x={x + 52} y={y + 16} fontFamily={FONT} fontSize="11.5" fontWeight="600" textAnchor="middle" fill="var(--onb-ink)">{t('onboarding.browser.guide.loadUnpacked')}</text>
    </g>
  )
  const title = (x, y) => <text x={x} y={y} fontFamily={FONT} fontSize="14" fontWeight="650" fill="var(--onb-ink)">{t('onboarding.browser.guide.extensions')}</text>
  const dev = (x, y) => <text x={x} y={y} fontFamily={FONT} fontSize="11" fill="var(--onb-ink-2)">{t('onboarding.browser.guide.devMode')}</text>
  return (
    <svg className="onb-browser-pic" viewBox="0 0 460 226" role="img" aria-label={label}>
      <rect x="34" y="12" width="412" height="202" rx="12" fill="oklch(0.15 0.04 288)" stroke="oklch(0.97 0.01 290 / .22)" />
      <path d="M34 50 V24 a12 12 0 0 1 12 -12 H434 a12 12 0 0 1 12 12 V50Z" fill="oklch(0.24 0.05 290)" />
      <rect x="72" y="21" width="300" height="20" rx="10" fill="oklch(0.12 0.03 288)" stroke="var(--onb-accent)" strokeWidth="1.5" />
      <text x="86" y="35" fontFamily={FONT} fontSize="11.5" fill="var(--onb-ink-2)">{addr}</text>
      <Mark n={1} x={396} y={31} />
      {left ? (
        <g>
          <rect x="34" y="62" width="120" height="152" fill="oklch(0.19 0.04 288)" />
          {dev(46, 166)}{toggle(46, 174)}
          {title(168, 86)}{load(168, 98)}
          <rect x="168" y="134" width="120" height="66" rx="8" fill="oklch(0.97 0.01 290 / .06)" />
          <rect x="296" y="134" width="120" height="66" rx="8" fill="oklch(0.97 0.01 290 / .06)" />
          <Mark n={2} x={98} y={181} /><Mark n={3} x={290} y={110} />
        </g>
      ) : (
        <g>
          {title(48, 88)}{dev(300, 88)}{toggle(392, 77)}{load(48, 100)}
          <rect x="48" y="136" width="176" height="66" rx="8" fill="oklch(0.97 0.01 290 / .06)" />
          <rect x="236" y="136" width="176" height="66" rx="8" fill="oklch(0.97 0.01 290 / .06)" />
          <Mark n={2} x={440} y={60} /><Mark n={3} x={168} y={112} />
        </g>
      )}
    </svg>
  )
}

const cut = (s) => (s.length > 16 ? s.slice(0, 15) + '…' : s)

// ProfileDiagram: MonoAgent profile <-> browser profile <-> logged-in sites.
export function ProfileDiagram({ profile }) {
  const { t } = useTranslation()
  const arrow = (x) => `M${x} 51h46M${x} 51l8 -6M${x} 51l8 6M${x + 46} 51l-8 -6M${x + 46} 51l-8 6`
  return (
    <svg className="onb-browser-pic" viewBox="0 0 520 104" role="img" aria-label={t('onboarding.browser.diagram.label', { profile })}>
      <g fontFamily="inherit" fontSize="13" textAnchor="middle">
        <rect x="4" y="22" width="140" height="58" rx="14" fill="oklch(0.83 0.15 72 / .14)" stroke="var(--onb-accent)" />
        <text x="74" y="46" fill="var(--onb-ink-3)" fontSize="11">{t('onboarding.browser.diagram.monoagent')}</text>
        <text x="74" y="65" fill="var(--onb-ink)" fontWeight="650">{cut(profile)}</text>
        <path d={arrow(150)} stroke="var(--onb-ink-3)" strokeWidth="2" fill="none" />
        <rect x="202" y="22" width="140" height="58" rx="14" fill="oklch(0.97 0.01 290 / .06)" stroke="var(--onb-line-2)" />
        <text x="272" y="56" fill="var(--onb-ink-2)" fontSize="12">{t('onboarding.browser.diagram.browser')}</text>
        <path d={arrow(348)} stroke="var(--onb-ink-3)" strokeWidth="2" fill="none" />
        <rect x="400" y="12" width="116" height="80" rx="14" fill="oklch(0.97 0.01 290 / .06)" stroke="var(--onb-line-2)" />
        <text x="458" y="32" fill="var(--onb-ink-3)" fontSize="11">{t('onboarding.browser.diagram.sites')}</text>
        <g fill="oklch(0.97 0.01 290 / .14)">
          <rect x="414" y="42" width="40" height="18" rx="5" /><rect x="460" y="42" width="40" height="18" rx="5" />
          <rect x="414" y="66" width="40" height="18" rx="5" /><rect x="460" y="66" width="40" height="18" rx="5" />
        </g>
      </g>
    </svg>
  )
}

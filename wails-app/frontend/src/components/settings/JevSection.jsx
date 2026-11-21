import { useState, useEffect } from 'react'
import { ChevronDown, ChevronUp, KeyRound, Zap, Loader2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { JevSetSurface, JevUseProvider } from '../../wailsjs/go/main/App'
import { mono, card, hint, errText, errMsg, keyChip, formatUSD } from './jevShared.jsx'
import { useJevData } from './useJevData.js'
import JevKeyRow, { useJevKey } from './JevKeyRow.jsx'
import JevFeaturesBlock from './JevFeaturesBlock.jsx'
import JevProvidersBlock from './JevProvidersBlock.jsx'

// TypeSafe Jev settings. Everything goes through `monoagentcli jev …` (Go
// side: app_jev.go); the key is sent once on Save and never read back.
// Enabling a surface first shows what it sends to TypeSafe and needs an
// explicit confirm — the GUI counterpart of `jev enable`'s prompt.
// The key row is JevKeyRow.jsx, the features and usage JevFeaturesBlock.jsx.

export { testError, Switch } from './jevShared.jsx'

// jev: the status and usage the page read once (useJevData) and shares between the machine and the profile part.
// Left out, the section reads them itself.
export default function JevSection(props = {}) {
  return props.jev ? <JevSectionView {...props} /> : <JevSectionOwn {...props} />
}

function JevSectionOwn(props) {
  return <JevSectionView {...props} jev={useJevData({ withUsage: props.part !== 'machine' })} />
}

// expandToken is what a jump to this section passes (Settings gives the navigation data, a new object for each
// navigation): the section opens whenever it changes to a value, also after the user folded it by hand, which
// defaultExpanded cannot do because it does not change from one jump to the next.
// part: 'machine' shows the machine-wide half (the API key), 'profile' the active profile's half (the features and
// their usage); left out, both in one section.
function JevSectionView({ collapsible = false, defaultExpanded = false, expandToken = null, part = 'all', jev }) {
  const { t } = useTranslation()
  const [isExpanded, setIsExpanded] = useState(defaultExpanded)
  useEffect(() => {
    if (defaultExpanded) setIsExpanded(true)
  }, [defaultExpanded])
  useEffect(() => {
    if (expandToken) setIsExpanded(true)
  }, [expandToken])

  const { status, loadErr, usage, usageErr, providers, providersErr, load } = jev
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [pending, setPending] = useState('')

  // run wraps one CLI call: one in flight at a time, errors shown inline
  // (below the key row, or where onErr shows them).
  const run = async (what, fn, onErr) => {
    setBusy(what); setErr('')
    try { return await fn() } catch (e) { (onErr || setErr)(errMsg(e)); return undefined } finally { setBusy('') }
  }
  const keyState = useJevKey(run, load)
  const setSurface = (surface, enabled, threshold) => run(`surface:${surface}`, async () => {
    await JevSetSurface(surface, enabled, threshold)
    setPending('')
    await load()
  })
  // name '' puts the surface back on the default provider.
  const setSurfaceProvider = (surface, name) => run(`surface:${surface}`, async () => {
    await JevUseProvider(name, surface)
    await load()
  })

  const header = (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
      <span style={{ fontFamily: mono, fontSize: 10, fontWeight: 700, color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: 2 }}>
        {part === 'profile' ? t('settings.jev.featuresTitle') : 'TypeSafe Jev'}
      </span>
      <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
    </div>
  )

  if (!status) {
    const loadingCard = (
      <div style={card}>
        {loadErr ? (
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <div style={{ ...errText, flex: 1 }}>Couldn't read Jev settings: {loadErr}</div>
            <button className="btn btn-secondary btn-sm" onClick={load}>Retry</button>
          </div>
        ) : (
          <div style={hint}>Loading Jev settings…</div>
        )}
      </div>
    )
    return (
      <div id={part === 'profile' ? 'settings-jev-features' : 'settings-jev'} data-testid={part === 'profile' ? 'jev-features-section' : 'jev-section'}>
        {header}
        {collapsible && part !== 'profile' ? (
          <>
            <div
              role="button"
              tabIndex={0}
              aria-expanded={isExpanded}
              data-testid="jev-fold-toggle"
              onClick={() => setIsExpanded(v => !v)}
              onKeyDown={e => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  setIsExpanded(v => !v)
                }
              }}
              style={{
                background: 'var(--surface)',
                border: `1px solid ${loadErr ? 'rgba(239,68,68,.35)' : 'var(--border)'}`,
                borderRadius: 'var(--radius-lg)',
                padding: '12px 18px',
                marginBottom: isExpanded ? 12 : 16,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                cursor: 'pointer',
                userSelect: 'none',
                transition: 'border-color 0.15s ease, background 0.15s ease',
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <Zap size={16} style={{ color: loadErr ? 'var(--red)' : 'var(--text-muted)', flexShrink: 0 }} />
                  <span style={{ fontFamily: mono, fontSize: 12, fontWeight: 700, color: 'var(--text)', letterSpacing: 0.5 }}>
                    TypeSafe Jev
                  </span>
                </div>
                {loadErr ? (
                  <span style={{
                    display: 'inline-flex', alignItems: 'center', gap: 5, fontFamily: mono, fontSize: 10.5,
                    color: 'var(--red)', background: 'rgba(239,68,68,.08)', border: '1px solid rgba(239,68,68,.25)',
                    borderRadius: 99, padding: '2px 9px',
                  }}>
                    Error reading settings
                  </span>
                ) : (
                  <span style={{
                    display: 'inline-flex', alignItems: 'center', gap: 5, fontFamily: mono, fontSize: 10.5,
                    color: 'var(--cyan)', background: 'rgba(0,180,216,.1)', border: '1px solid rgba(0,180,216,.25)',
                    borderRadius: 99, padding: '2px 9px',
                  }}>
                    <Loader2 size={11} className="spin" style={{ color: 'var(--cyan)' }} /> Loading…
                  </span>
                )}
              </div>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                {isExpanded ? <ChevronUp size={16} style={{ color: 'var(--text-muted)' }} /> : <ChevronDown size={16} style={{ color: 'var(--text-muted)' }} />}
              </div>
            </div>

            {isExpanded && loadingCard}
          </>
        ) : loadingCard}
      </div>
    )
  }

  const chip = keyChip(status, t)
  const hasKey = status.key_source && status.key_source !== 'none'
  const activeCount = (status.surfaces || []).filter(s => s.enabled).length
  const machine = part === 'machine'

  const featuresBlock = (
    <JevFeaturesBlock
      part={part} status={status} usage={usage} usageErr={usageErr} busy={busy} pending={pending}
      onPending={setPending} onClearErr={() => setErr('')} onSetSurface={setSurface}
      providers={providers} onSetSurfaceProvider={setSurfaceProvider}
    />
  )

  if (part === 'profile') {
    return (
      <div id="settings-jev-features" data-testid="jev-features-section">
        {header}
        <div style={card}>
          {err && <div role="alert" style={errText}>{err}</div>}
          {featuresBlock}
        </div>
      </div>
    )
  }

  return (
    <div id="settings-jev" data-testid="jev-section">
      {header}
      {collapsible && (
        <div
          role="button"
          tabIndex={0}
          aria-expanded={isExpanded}
          data-testid="jev-fold-toggle"
          onClick={() => setIsExpanded(v => !v)}
          onKeyDown={e => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault()
              setIsExpanded(v => !v)
            }
          }}
          style={{
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: 'var(--radius-lg)',
            padding: '12px 18px',
            marginBottom: isExpanded ? 12 : 16,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            cursor: 'pointer',
            userSelect: 'none',
            transition: 'border-color 0.15s ease, background 0.15s ease',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <Zap size={16} style={{ color: hasKey ? 'var(--cyan)' : 'var(--text-muted)', flexShrink: 0 }} />
              <span style={{ fontFamily: mono, fontSize: 12, fontWeight: 700, color: 'var(--text)', letterSpacing: 0.5 }}>
                TypeSafe Jev
              </span>
            </div>

            <span
              data-testid="jev-fold-key-chip"
              style={{
                display: 'inline-flex', alignItems: 'center', gap: 5, fontFamily: mono, fontSize: 10.5,
                color: chip.color, background: chip.bg, border: `1px solid ${chip.bd}`, borderRadius: 99, padding: '2px 9px',
              }}
            >
              <KeyRound size={11} /> {chip.text}
            </span>

            {!machine && <span
              data-testid="jev-fold-features-badge"
              style={{
                display: 'inline-flex', alignItems: 'center', gap: 5, fontFamily: mono, fontSize: 10.5,
                color: activeCount > 0 ? 'var(--green-neon)' : 'var(--text-muted)',
                background: activeCount > 0 ? 'rgba(16,185,129,.1)' : 'rgba(255,255,255,.05)',
                border: `1px solid ${activeCount > 0 ? 'rgba(74,222,128,.25)' : 'var(--border)'}`,
                borderRadius: 99, padding: '2px 9px',
              }}
            >
              {activeCount} {activeCount === 1 ? 'feature' : 'features'} active
            </span>}
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            {!machine && usage?.total?.calls ? (
              <span style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-muted)' }}>
                {usage.total.calls} {usage.total.calls === 1 ? 'call' : 'calls'} (7d) · {formatUSD(usage.total.estimated_usd)}
              </span>
            ) : null}
            {isExpanded ? (
              <ChevronUp size={16} style={{ color: 'var(--text-muted)' }} />
            ) : (
              <ChevronDown size={16} style={{ color: 'var(--text-muted)' }} />
            )}
          </div>
        </div>
      )}

      {(!collapsible || isExpanded) && (
        <div style={card}>
        <div style={hint}>
          Fast pick-one and yes/no decisions for a few monoagent features. Jev never writes text,
          and nothing is sent to TypeSafe until you turn a feature on{machine ? " in a profile's settings" : ' below'}. Get an API key at console.typesafe.ai.
        </div>

        <JevProvidersBlock status={status} providers={providers} providersErr={providersErr} busy={busy} run={run} reload={load} />

        <JevKeyRow status={status} busy={busy} k={keyState} />

        {err && <div role="alert" style={errText}>{err}</div>}

        {!machine && featuresBlock}
      </div>
      )}
    </div>
  )
}

import { useTranslation } from 'react-i18next'
import { AsciiHost, Section, box, displayOrigin, requesterText } from './BrowserSubmitDetails.jsx'
import i18nOr from '../../lib/i18nOr.js'

// A browser site approval (plan C8): an agent whose kind is at "Ask per new
// site" tried to use a site with no rule. readonly_data is {origin, url,
// kind, requested_by, asciiHost?, unicodeHost?}. Approving it (Allow) adds an allow rule for the
// origin, which every agent kind then honours; rejecting it (Not now) adds
// nothing.

export const BROWSER_SITE = 'browser.site'

export function isBrowserSite(item) {
  return item?.node_config?.kind === BROWSER_SITE
}

const WHO = { coder: 'ui6SetHil.who_coder', org: 'ui6SetHil.who_org', chat: 'ui6SetHil.who_chat', other: 'ui6SetHil.who_other' }

export function BrowserSiteDetails({ item }) {
  useTranslation() // render again on a language change
  const ro = item.readonly_data || {}
  const who = requesterText(ro.requested_by)
  return (
    <div data-testid="browser-site-details" style={{ marginBottom: 20 }}>
      <div style={{ fontSize: 14, color: '#e2e8f0', marginBottom: 12, wordBreak: 'break-all' }}>
        {i18nOr(WHO[ro.kind] || WHO.other)} {i18nOr('ui6SetHil.wants_to_use')} <strong>{displayOrigin(ro) || i18nOr('ui6SetHil.a_site')}</strong>
        <AsciiHost host={ro.asciiHost} />
      </div>
      {ro.url && (
        <Section label={i18nOr('ui6SetHil.page')}>
          <div style={{ ...box, fontFamily: 'var(--font-mono)', fontSize: 11 }}>{ro.url}</div>
        </Section>
      )}
      <Section label={i18nOr('ui6SetHil.requested_by')}>
        <div style={box}>{who}</div>
      </Section>
      <div style={{ fontSize: 11, color: 'var(--text-muted)', lineHeight: 1.5 }}>
        {i18nOr('ui6SetHil.site_note')}
      </div>
    </div>
  )
}

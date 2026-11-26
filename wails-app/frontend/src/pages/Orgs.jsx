import OrgsPanel from '../components/OrgsPanel.jsx'
import { useTranslation } from 'react-i18next'

// isActive tells the panel whether the Orgs page is the app's active tab —
// App.jsx keeps pages mounted (display:none) after first visit, so the
// panel needs it to stop its SSE stream while another page is showing.
// onNavigate is App.jsx's page switcher — passed through so OrgsPanel can
// bring the user here itself when an org gets created from elsewhere (the
// Chat panel, via Bash/monoagentcli) while they're on a different page —
// see OrgsPanel's onOrgDesignUpdated listener. pendingSelectOrgName /
// onConsumePendingSelect carry the same signal from App.jsx's OWN
// top-level listener, needed because this whole page (and OrgsPanel's
// listener) doesn't mount until the user visits Orgs at least once — for
// the very first org created before that, App.jsx is the only thing
// around to have caught the event, so it hands the name off here instead.
export default function Orgs({ isActive = true, onNavigate, pendingSelectOrgName, onConsumePendingSelect }) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      <div className="page-header">
        <div className="page-header-left">
          <div className="page-title">{t('ui6Orgs.page_title')}</div>
          <div className="page-subtitle">{t('ui6Orgs.page_subtitle')}</div>
        </div>
      </div>
      <div className="page-body" style={{ flex: 1, overflow: 'hidden', display: 'flex' }}>
        <OrgsPanel
          pageActive={isActive}
          onNavigate={onNavigate}
          pendingSelectOrgName={pendingSelectOrgName}
          onConsumePendingSelect={onConsumePendingSelect}
        />
      </div>
    </div>
  )
}

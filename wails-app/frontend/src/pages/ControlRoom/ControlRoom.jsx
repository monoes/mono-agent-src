import { useTranslation } from 'react-i18next'
import { controlRoomApi } from '../../services/controlRoom.js'
import { hasData } from './controlRoomModel.js'
import { useControlRoom } from './useControlRoom.js'
import RunHeader from './RunHeader.jsx'
import BadChainBanner from './BadChainBanner.jsx'
import UnanchoredBanner from './UnanchoredBanner.jsx'
import TreeClusters from './TreeClusters.jsx'
import SpendMeter from './SpendMeter.jsx'
import DecisionFeed from './DecisionFeed.jsx'
import StopPanel from './StopPanel.jsx'
import ShowBossLink from '../../components/agents/ShowBossLink.jsx'
import './controlRoom.css'

// The doomsday control room: reads `doomsday overview` and `doomsday feed`, offers one action, Stop. It never starts
// a run and has no approve, pause or resume. Nothing here is a healthy state unless the audit chain verified.
export default function ControlRoom({ isActive = true, api = controlRoomApi }) {
  const { t } = useTranslation()
  const { doc, view, lastRun, feed, rate, loadOlder, refresh } = useControlRoom(isActive, api)
  const run = view.state === 'error' ? lastRun?.run : doc?.run
  // Stop takes no argument, so it is offered whenever a run may exist: in every ok state, under every error banner
  // and during the very first poll (doc null reads as an error view). With no known run it says "run state unknown".
  const showStop = view.state === 'ok' || view.state === 'error'
  const runUnknown = view.state === 'error' && !lastRun
  // Stop is disabled only when a poll whose chain verified (yes) says the run is not active. Any other chain, any
  // error and a stale last-known run keep it enabled: a forged or stale ended row must not lock the operator out.
  const stopDisabled = view.state === 'ok' && view.chain === 'yes' && !!run && !run.active
  return (
    <div className="cr-page" data-testid="cr-page">
      <div className="page-header">
        <div className="page-header-left">
          <h1 className="page-title">{t('controlRoom.title')}</h1>
          <p className="page-subtitle">{t('controlRoom.subtitle')}</p>
        </div>
      </div>
      <div className="page-body">
        {doc === null ? <p className="cr-meta" data-testid="cr-loading">{t('controlRoom.loading')}</p> : null}
        {view.state === 'off' ? <p className="cr-meta" data-testid="cr-off">{t('controlRoom.off')}</p> : null}
        {view.state === 'no_run' ? (
          <div data-testid="cr-no-run">
            <p className="cr-meta">{t('controlRoom.noRun')}</p>
            <p className="cr-meta">{t('controlRoom.noRunHow')}</p>
            <pre className="cr-meta" data-testid="cr-no-run-hint">{t('controlRoom.noRunCommands')}</pre>
          </div>
        ) : null}
        {view.state === 'error' && doc !== null ? (
          <p className="cr-warn" role="alert" data-testid="cr-error" data-error-code={view.errorCode || ''}>
            {view.errorCode ? t(`controlRoom.errorCode.${view.errorCode}`) : t('controlRoom.error', { message: view.message })}
          </p>
        ) : null}
        {view.state === 'ok' ? (
          <>
            <RunHeader run={run} integrity={doc.integrity} />
            {run?.active ? <div style={{ margin: '6px 0' }}><ShowBossLink /></div> : null}
            {view.chain === 'unanchored' ? <UnanchoredBanner reason={doc.integrity?.reason} /> : null}
            {!hasData(view.chain) ? <BadChainBanner chain={view.chain} reason={doc.integrity?.reason} /> : (
              <div className="cr-grid">
                <TreeClusters tree={doc.tree} />
                <SpendMeter spend={doc.spend} rate={rate} />
                <DecisionFeed feed={feed} onLoadOlder={loadOlder} />
              </div>
            )}
          </>
        ) : null}
        {showStop ? <StopPanel api={api} disabled={stopDisabled} unknown={runUnknown} onDone={refresh} /> : null}
      </div>
    </div>
  )
}

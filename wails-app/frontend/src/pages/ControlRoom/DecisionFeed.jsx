import { useTranslation } from 'react-i18next'
import { feedClass, formatMicro, clip, num, FEED_KEEP } from './controlRoomModel.js'

// The boss's decisions, newest first: the class, the operation and result, amounts, the approval reference.
// The CLI sends no text of an answer, and none is drawn here.
export default function DecisionFeed({ feed, onLoadOlder }) {
  const { t } = useTranslation()
  const items = feed?.items || []
  return (
    <section className="cr-card" aria-label={t('controlRoom.feed.title')} data-testid="cr-feed">
      <h3>{t('controlRoom.feed.title')}</h3>
      {items.length === 0 ? <p className="cr-meta">{t('controlRoom.feed.empty')}</p> : (
        <ol className="cr-feed">
          {items.map(r => {
            const cls = feedClass(r.class)
            return (
              <li key={r.seq} data-testid="cr-feed-row" data-class={cls}>
                <span className="cr-mono cr-seq">#{num(r.seq)}</span>
                <span className={`cr-badge cr-class-${cls}`}>{t(`controlRoom.feed.class.${cls}`)}</span>
                <span className="cr-mono">{clip(r.op || r.kind, 48)}</span>
                {r.result ? <span className="cr-meta">{clip(r.result, 32)}</span> : null}
                {typeof r.amount_micro === 'number' ? <span>{formatMicro(r.amount_micro)}</span> : null}
                {typeof r.count === 'number' ? <span className="cr-meta">×{r.count}</span> : null}
                {r.answer_class ? <span className="cr-meta">{clip(r.answer_class, 32)}</span> : null}
                {Array.isArray(r.exceeded) && r.exceeded.length > 0 ? <span className="cr-warn">{t('controlRoom.feed.exceeded', { what: r.exceeded.slice(0, 5).map(x => clip(x, 40)).join(', ') })}</span> : null}
                {r.ref ? <code className="cr-mono cr-meta">{clip(r.ref, 40)}</code> : null}
              </li>
            )
          })}
        </ol>
      )}
      {feed?.next && items.length < FEED_KEEP ? (
        <button type="button" className="btn btn-ghost btn-sm" onClick={onLoadOlder}>{t('controlRoom.feed.older')}</button>
      ) : null}
    </section>
  )
}

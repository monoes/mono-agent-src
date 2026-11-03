import { useTranslation } from 'react-i18next'
import { clusterRows, num } from './controlRoomModel.js'

// Members by kind, never one entry per member: at most MAX_CLUSTERS rows however many members there are.
export default function TreeClusters({ tree }) {
  const { t } = useTranslation()
  const rows = clusterRows(tree)
  const widest = Math.max(1, ...rows.map(r => r.total))
  return (
    <section className="cr-card" aria-label={t('controlRoom.tree.title')} data-testid="cr-tree">
      <h3>{t('controlRoom.tree.title')}</h3>
      <p className="cr-meta">{t('controlRoom.tree.counts', { live: num(tree?.live), total: num(tree?.total) })}</p>
      {rows.length === 0 ? <p className="cr-meta">{t('controlRoom.tree.empty')}</p> : (
        <ul className="cr-clusters">
          {rows.map(r => (
            <li key={r.kind} data-testid="cr-cluster">
              <span className="cr-mono cr-cluster-kind">{r.kind}</span>
              <span className="cr-bar" aria-hidden="true"><span className="cr-bar-fill" style={{ width: `${(r.total / widest) * 100}%` }}><span className="cr-bar-live" style={{ width: `${r.total ? (Math.min(r.live, r.total) / r.total) * 100 : 0}%` }} /></span></span>
              <span className="cr-meta">{t('controlRoom.tree.row', { live: r.live, total: r.total })}</span>
            </li>
          ))}
        </ul>
      )}
      {tree?.truncated ? <p className="cr-meta">{t('controlRoom.tree.truncated')}</p> : null}
    </section>
  )
}

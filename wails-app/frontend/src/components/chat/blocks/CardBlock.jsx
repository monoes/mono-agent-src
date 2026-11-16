import { panel, heading, COLOR } from './ui.js'
import { AppLinkButton } from './AppLinkButton.jsx'

const TONE = { ok: COLOR.ok, warn: COLOR.warn, error: COLOR.error, info: COLOR.accent, neutral: COLOR.muted }

function Entity({ e, bare }) {
  return (
    <div style={bare ? { padding: '6px 0', borderTop: `1px solid ${COLOR.border}` } : panel}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 9, color: COLOR.muted, textTransform: 'uppercase', letterSpacing: 0.5 }}>{e.kind}</div>
          <div style={{ fontSize: 12, fontWeight: 600, color: COLOR.text, wordBreak: 'break-word' }}>{e.title}</div>
          {e.subtitle && <div style={{ fontSize: 10, color: COLOR.muted, wordBreak: 'break-word' }}>{e.subtitle}</div>}
        </div>
        {e.status && (
          <span style={{ fontSize: 10, color: TONE[e.tone], border: `1px solid ${TONE[e.tone]}66`, borderRadius: 10, padding: '0 7px', whiteSpace: 'nowrap' }}>{e.status}</span>
        )}
        {e.link && <AppLinkButton href={e.link} />}
      </div>
    </div>
  )
}

// card: one entity. list: several, under an optional title.
export function CardBlock({ data }) {
  return <Entity e={data} />
}

export function ListBlock({ data }) {
  return (
    <div style={panel}>
      {data.title && <p style={heading}>{data.title}</p>}
      <div role="list">
        {data.items.map((e, i) => <div role="listitem" key={i}><Entity e={e} bare /></div>)}
      </div>
    </div>
  )
}

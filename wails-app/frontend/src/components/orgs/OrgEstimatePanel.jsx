// monomind's pre-run cost estimate (`org estimate`, with its "stale rates"
// note) for an org that never ran. Renders nothing while the CLI cannot give
// one (unsigned, `org serve` running, ...); signing the org reads it again.
import { useEffect, useState } from 'react'
import { api } from '../../services/api.js'
import { onSignatureRefresh } from './OrgSignatureBanner.jsx'
import { sectionLabel, mutedText } from './ui.jsx'

export default function OrgEstimatePanel({ orgName }) {
  const [estimate, setEstimate] = useState(null)
  const [reads, setReads] = useState(0)
  useEffect(() => onSignatureRefresh(orgName, () => setReads(n => n + 1)), [orgName])
  useEffect(() => {
    let cancelled = false
    Promise.resolve(api.getOrgEstimate(orgName)).then(res => { if (!cancelled) setEstimate(res && !res.error && res.text ? res : null) })
    return () => { cancelled = true }
  }, [orgName, reads])
  if (!estimate) return null
  return (
    <div data-testid="org-estimate" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div style={sectionLabel}>Estimate before the first run</div>
      <pre style={{ ...mutedText, margin: 0, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', padding: '6px 8px', border: '1px solid var(--border)', borderRadius: 'var(--radius)', background: 'var(--surface)' }}>{estimate.text}</pre>
    </div>
  )
}

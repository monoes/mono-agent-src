import { useEffect, useMemo, useState } from 'react'
import { api, notify } from '../../services/api.js'
import { detectArtifactCandidate, resolveArtifact, openArtifact, artifactTarget } from './chatArtifacts.js'
import { ToolLink } from './ToolLink.jsx'

// The tab each kind of entity opens in (toolLinks.page.* names it).
const PAGE_OF = { workflow: 'noderunner', org: 'orgs', document: 'documents' }

// ToolEntityLink is "Open in <tab>" for a tool card whose result names a real
// entity (create_workflow, create_org, save_document). The reference comes from
// the tool's own result, so it is looked up in the backend before the link
// shows, and again when it is clicked (chatArtifacts.openArtifact).
export function ToolEntityLink({ call, onNavigate }) {
  const candidate = useMemo(() => detectArtifactCandidate(call), [call])
  const page = candidate && PAGE_OF[candidate.type]
  const [artifact, setArtifact] = useState(null)
  useEffect(() => {
    if (!page || !onNavigate) return undefined
    let live = true
    resolveArtifact(candidate, api).catch(() => null).then(a => { if (live) setArtifact(a) })
    return () => { live = false }
  }, [candidate, page, !!onNavigate]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!artifact || !onNavigate) return null
  const open = () => openArtifact(call, artifact, {
    api, notify,
    onOpen: (fresh) => { const t = artifactTarget(fresh); if (t) onNavigate(t.page, t.data) },
  })
  return <div style={{ padding: '0 10px 6px' }}><ToolLink link={{ page }} onNavigate={open} /></div>
}

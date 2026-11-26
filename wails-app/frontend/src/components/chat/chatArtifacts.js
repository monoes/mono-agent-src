// Chat result "artifacts" — click-to-open actions for a small, explicit
// allowlist of tool results (plan §Task 6, "Result actions that exist
// today"). Two-phase, deliberately:
//
//   detectArtifactCandidate — pure, synchronous. Looks at one completed
//   tool call and extracts an UNVALIDATED reference straight from the
//   tool's own self-reported result. Safe against forged/malformed/
//   unsupported results entirely on its own (never throws, never trusts
//   the value beyond "right shape, right type").
//
//   resolveArtifact — async. Re-validates that reference against a real,
//   profile-scoped backend lookup before it is ever trusted enough to
//   navigate anywhere or open a file (plan: "resolved to trusted
//   metadata"). A forged id, a deleted entity, or one belonging to a
//   different profile all resolve to null here, same as an id that was
//   never real to begin with.
//
// A tool call that doesn't produce a still-valid artifact is left to
// render as a normal ToolActivityCard — this module only ever adds a
// small extra affordance next to that, never replaces it (plan: "Keep
// generic tool output as fallback").
//
// Literal tool names and result shapes below are taken directly from
// internal/ai/chat/monoagent_tools.go (createWorkflow/createOrg/
// saveDocument) — see that file if either ever changes shape.
import i18nOr from '../../lib/i18nOr.js'

const ALLOWED_TOOL_NAMES = new Set(['create_workflow', 'create_org', 'save_document', 'save_image'])
const TYPE_KEYS = { workflow: 'ui7Chat.artifact_workflow', org: 'ui7Chat.artifact_org', document: 'ui7Chat.artifact_document', image: 'ui7Chat.artifact_image' }

function nonEmptyString(v) {
  return typeof v === 'string' && v.trim().length > 0
}

export function detectArtifactCandidate(call) {
  if (!call || !ALLOWED_TOOL_NAMES.has(call.name)) return null
  // Not completed yet, or the tool call itself failed — no real artifact
  // to offer either way. `ok === null` (some adapters never report an
  // explicit outcome) is treated as not-failed, same tri-state reading
  // ToolActivityCard itself uses — resolveArtifact's own backend check is
  // the actual authority regardless.
  if (call.status !== 'completed' || call.ok === false) return null
  if (typeof call.result !== 'string') return null

  let parsed
  try {
    parsed = JSON.parse(call.result)
  } catch {
    return null
  }
  if (!parsed || typeof parsed !== 'object') return null

  if (call.name === 'create_workflow') {
    return nonEmptyString(parsed.workflow_id)
      ? { type: 'workflow', callId: call.callId, id: parsed.workflow_id }
      : null
  }
  if (call.name === 'create_org') {
    return nonEmptyString(parsed.org_name)
      ? { type: 'org', callId: call.callId, name: parsed.org_name }
      : null
  }
  if (call.name === 'save_document') {
    // vault_document_id is only present when the tool's own best-effort
    // vault registration succeeded — absent is a normal, expected case
    // (the file was still written), not a malformed result.
    return nonEmptyString(parsed.vault_document_id)
      ? { type: 'document', callId: call.callId, id: parsed.vault_document_id }
      : null
  }
  if (call.name === 'save_image') {
    return nonEmptyString(parsed.vault_image_id)
      ? { type: 'image', callId: call.callId, id: parsed.vault_image_id }
      : null
  }
  return null
}

// resolveArtifact takes `api` explicitly (services/api.js's singleton, in
// practice) rather than importing it, so this stays testable with a small
// hand-built fake instead of mocking the whole api module.
export async function resolveArtifact(candidate, api) {
  if (!candidate) return null

  if (candidate.type === 'workflow') {
    // GetWorkflow (wails-app/app_workflows.go) does a real profile_id
    // comparison and returns "workflow %s not found" for a deleted or
    // cross-profile id — api.getWorkflow's own guard() turns that
    // rejection into null.
    const wf = await api.getWorkflow(candidate.id)
    if (!wf || !nonEmptyString(wf.id)) return null
    return { type: 'workflow', id: wf.id, name: nonEmptyString(wf.name) ? wf.name : wf.id }
  }

  if (candidate.type === 'org') {
    // No single "does this org exist" lookup exists — validated the same
    // way App.jsx's own org-watcher bookkeeping already does: check the
    // name against the real, profile-scoped org listing (plan:
    // "organization name validated through existing org listing").
    const res = await api.listOrgDesigns()
    const items = Array.isArray(res) ? res : (res?.items || [])
    const found = items.some(o => o?.name === candidate.name)
    return found ? { type: 'org', name: candidate.name } : null
  }

  if (candidate.type === 'document') {
    // getProfileDocument is a single-row, profile-scoped lookup (mirrors
    // GetWorkflow's shape) rather than fetching every document in the
    // vault and filtering client-side — a cross-profile or deleted id
    // resolves to null the same way a forged id (e.g. a path-traversal-
    // shaped string) never matches any real row.
    const doc = await api.getProfileDocument(candidate.id)
    if (!doc) return null
    return { type: 'document', id: doc.id, filename: doc.filename, path: doc.path, sizeBytes: doc.size_bytes }
  }

  if (candidate.type === 'image') {
    const img = await api.getVaultImage(candidate.id)
    if (!img || !nonEmptyString(img.id)) return null
    return { type: 'image', id: img.id, filename: img.filename, label: img.label || img.filename || img.id, path: img.path, url: img.url }
  }

  return null
}

// openArtifact re-validates a resolved artifact's underlying reference
// right before acting on it, rather than trusting useResolvedArtifacts.js's
// own cache — that cache never expires, so a workflow/org/document deleted
// after its card first resolved would otherwise still offer a stale
// Open/Copy-ID action. A generic "revalidate before acting" operation with
// no panel-specific dependency: `api` and `notify` are passed in explicitly
// (same reasoning as resolveArtifact's own explicit `api` parameter above —
// this file imports only the translator, so it stays testable with small hand-built
// fakes instead of mocking services/api.js), and `onOpen` is whatever the
// caller wants to do with a freshly-confirmed artifact.
//
// Reuses detectArtifactCandidate off the live `call` (not the stale cached
// `artifact`) so a cross-profile or genuinely-deleted target is caught here
// even though the card was legitimately valid when it first appeared.
export async function openArtifact(call, artifact, { api, notify, onOpen }) {
  const candidate = detectArtifactCandidate(call)
  if (!candidate) return
  const fresh = await resolveArtifact(candidate, api).catch(() => null)
  if (!fresh) {
    // A null result here means either "genuinely gone" or "the lookup
    // itself failed" — api.js's guard() swallows real backend errors into
    // the same null/[] shape a clean not-found produces, so this can't
    // claim deletion specifically without risking a false "no longer
    // exists" on a mere transient failure (a real failure also already
    // gets its own toast from guard's reportError). Reads `artifact.type`
    // (the cached/rendered artifact passed in), not `candidate.type` — the
    // candidate is a fresh re-derivation from `call` and happens to agree
    // with `artifact.type` today, but `artifact` is the thing the user
    // actually saw and clicked.
    notify('chat', i18nOr('ui7Chat.artifact_gone', { type: Object.hasOwn(TYPE_KEYS, artifact.type) ? i18nOr(TYPE_KEYS[artifact.type]) : artifact.type }))
    return
  }
  onOpen?.(fresh)
}

// artifactTarget: the navigate(page, data) a resolved artifact opens in its
// tab, or null for one with no tab of its own here (an image opens through
// the artifact card). Same destinations as App.jsx's onOpenArtifact.
export function artifactTarget(artifact) {
  if (artifact?.type === 'workflow') return { page: 'noderunner', data: { workflowId: artifact.id } }
  if (artifact?.type === 'org') return { page: 'orgs', data: { org: artifact.name } }
  if (artifact?.type === 'document') return { page: 'documents', data: null }
  return null
}

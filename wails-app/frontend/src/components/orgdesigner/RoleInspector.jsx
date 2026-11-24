import { useEffect, useRef, useState } from 'react'
import { Crown } from 'lucide-react'
import { wouldCycle } from './orgGraph.js'
import { iconUrl } from './roleIcons.js'
import { KVBlock } from '../KVBlock.jsx'
import StringListField from '../StringListField.jsx'
import RoleAutomationsSection, { EffectiveTools } from './RoleAutomationsSection.jsx'
import RoleFullAccessSection from './RoleFullAccessSection.jsx'
import { declaresFull } from './fullAccess.jsx'
import RuntimeSelect from './RuntimeSelect.jsx'
import { RUNTIME_OPTIONS, MODELED_REST_FIELDS, MODELED_POLICY_FIELDS, omit, cleanObj, numOrUndefined } from './roleInspectorModel.js'
import { ModelBudgetsSection, ProviderOverrideSection, ToolPolicySection, InstructionsFileSection } from './RoleInspectorSections.jsx'


/**
 * RoleInspector — the detail/edit panel for whichever role is currently
 * selected on the Org Designer canvas. Pure "leaf" component: it never
 * mutates `node` itself, it only reports intent upward.
 *
 * @param {object|null} node Canvas node shape from orgGraph.js's hydrate()
 *   ({id, title, type, parentId, responsibilities, icon, color, x, y, rest}),
 *   or null when nothing is selected (renders an empty-state placeholder).
 * @param {object[]} allNodes Full array of canvas nodes — used to populate
 *   the "reports to" dropdown and to exclude cycle-forming candidates.
 * @param {(patch: object) => void} onPatch Partial update, e.g. {title: 'x'}.
 *   The parent/container applies it (merging into node + persisting).
 *   Special key `_renameId: string` requests an id rename; the container
 *   must cascade that to every child's parentId, which this component does
 *   not (and cannot, from a single node's perspective) do itself.
 * @param {(parentId: string|null) => void} onSetReportsTo Reparents this role
 *   (or, with null, promotes it to root when the org has no other root —
 *   the "Reports to" dropdown's plain path; see onPromoteToRoot for the
 *   general "swap boss" case).
 * @param {() => void} onPromoteToRoot Called when the user clicks "Set as
 *   org boss" — makes this role the root even when a different root already
 *   exists (the parent reverses the whole path between them, see
 *   orgdesign.Doc.PromoteToRoot). Only shown/enabled when this role isn't
 *   already root.
 * @param {() => void} onOpenIconPicker Called when the avatar is clicked;
 *   opens the single shared IconPickerModal instance (owned by the parent).
 * @param {() => void} onDelete Called when the user asks to delete this role;
 *   the parent/container owns the actual DeleteRoleModal flow.
 */
export default function RoleInspector({
  node, allNodes, onPatch, onSetReportsTo, onPromoteToRoot, onOpenIconPicker, onDelete,
  // Automation props (optional — the inspector still works without them):
  orgName, grants, automations, engineOffline, grantsVersion, onGrantsChanged, onOpenWorkflow, onEditGrant,
  // Full access (#205): this role's `org status` roles_access entry, if any.
  // orgRuntime is the org's default runtime, which a role without its own
  // runs on.
  fullAccess = null, onAccessChanged, orgRuntime = '', sectionsOrg = false,
}) {
  const [title, setTitle] = useState(node?.title || '')
  const [label, setLabel] = useState('')
  const [responsibilities, setResponsibilities] = useState(node?.responsibilities || [])
  const [renaming, setRenaming] = useState(false)
  const [renameValue, setRenameValue] = useState('')
  const [model, setModel] = useState('')
  const [providerName, setProviderName] = useState('')
  const [maxTurns, setMaxTurns] = useState('')
  const [budgetTokens, setBudgetTokens] = useState('')
  const [budgetUsd, setBudgetUsd] = useState('')
  const [instructionsFile, setInstructionsFile] = useState('')

  // Provider override (the inline `provider` block — distinct from
  // `providerName`/adapter_config.provider, a reference to a provider
  // configured elsewhere via `monomind providers configure`).
  const [pKind, setPKind] = useState('')
  const [pVendor, setPVendor] = useState('')
  const [pApiKeyEnv, setPApiKeyEnv] = useState('')
  const [pBaseUrl, setPBaseUrl] = useState('')
  const [pAuthTokenEnv, setPAuthTokenEnv] = useState('')

  // Tool policy.
  const [policyGit, setPolicyGit] = useState('read')
  const [policyMaxTokens, setPolicyMaxTokens] = useState('')
  const [policyMaxUsd, setPolicyMaxUsd] = useState('')
  const [allowTools, setAllowTools] = useState([])
  const [denyTools, setDenyTools] = useState([])
  const [fileWrite, setFileWrite] = useState([])
  const [fileRead, setFileRead] = useState([])
  const [webAllow, setWebAllow] = useState([])
  const [autoApproveTools, setAutoApproveTools] = useState([])

  // Remount key for the StringListField instances below (responsibilities +
  // the six tool-policy arrays). Deliberately its OWN state, set inside this
  // same effect rather than reading node?.id directly at the call sites:
  // node?.id changes the INSTANT the node prop changes (same render), but
  // the array state below (responsibilities, allowTools, ...) only catches
  // up one render later, once this effect actually runs. Keying the remount
  // on node?.id directly would therefore force the remount one render too
  // early -- while the array props are still the PREVIOUS role's data --
  // and by the time the correct data lands, the key is no longer changing,
  // so it never reaches the (already mounted with stale defaultValue) DOM.
  // Setting this in the same effect as the array state guarantees both land
  // in the same batched re-render, so the remount and the correct values
  // always arrive together.
  const [syncedId, setSyncedId] = useState(node?.id ?? null)
  const [policyResetGen, setPolicyResetGen] = useState(0)

  useEffect(() => {
    setSyncedId(node?.id ?? null)
    setTitle(node?.title || '')
    setResponsibilities(node?.responsibilities || [])
    setRenaming(false)
    setRenameValue(node?.id || '')
    setLabel(node && node.parentId != null ? (node.type || '') : '')

    const adapterConfig = node?.rest?.adapter_config || {}
    setModel(adapterConfig.model ?? '')
    setProviderName(adapterConfig.provider ?? '')
    setMaxTurns(node?.rest?.max_turns_per_message ?? '')
    setBudgetTokens(node?.rest?.budget_tokens ?? '')
    setBudgetUsd(node?.rest?.budget_usd ?? '')
    setInstructionsFile(node?.rest?.instructions_file ?? '')

    const provider = node?.rest?.provider || {}
    setPKind(provider.kind ?? '')
    setPVendor(provider.vendor ?? '')
    setPApiKeyEnv(provider.apiKeyEnv ?? '')
    setPBaseUrl(provider.baseUrl ?? '')
    setPAuthTokenEnv(provider.authTokenEnv ?? '')

    const policy = node?.rest?.policy || {}
    setPolicyGit(policy.git ?? 'read')
    setPolicyMaxTokens(policy.maxTokens ?? '')
    setPolicyMaxUsd(policy.maxUsd ?? '')
    setAllowTools(policy.allowTools || [])
    setDenyTools(policy.denyTools || [])
    setFileWrite(policy.fileWrite || [])
    setFileRead(policy.fileRead || [])
    setWebAllow(policy.webAllow || [])
    setAutoApproveTools(policy.autoApproveTools || [])
    // Deliberately keyed on node?.id, NOT the node object itself: OrgDesigner
    // rebuilds a fresh node object (new reference, same id) for every role on
    // every load()/applyLivePatch()/refreshFromServer() round trip -- including
    // the one triggered by saving a *different* field on this *same* role, or
    // an unrelated live org-update event. Neither is gated by isInteractingRef
    // (that guard only covers canvas drag, never these text fields). Keying on
    // the full object would re-run this reset on every such reference churn,
    // silently discarding whatever the user is mid-typing into another field.
    // Keying on id means: reset when the SELECTED ROLE changes, never just
    // because its object reference did.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [node?.id])

  // Tool policy alone also re-syncs when its saved value changes for the same
  // role (e.g. "Deny Bash" from the grant dialog), so a later policy edit
  // here never writes back a stale denyTools list. Other fields keep the
  // id-only rule above.
  const policyKey = JSON.stringify(node?.rest?.policy ?? null)
  const firstPolicySync = useRef(true)
  useEffect(() => {
    if (firstPolicySync.current) { firstPolicySync.current = false; return }
    const policy = node?.rest?.policy || {}
    setPolicyGit(policy.git ?? 'read')
    setPolicyMaxTokens(policy.maxTokens ?? '')
    setPolicyMaxUsd(policy.maxUsd ?? '')
    setAllowTools(policy.allowTools || [])
    setDenyTools(policy.denyTools || [])
    setFileWrite(policy.fileWrite || [])
    setFileRead(policy.fileRead || [])
    setWebAllow(policy.webAllow || [])
    setAutoApproveTools(policy.autoApproveTools || [])
    setPolicyResetGen(g => g + 1)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [policyKey])

  if (!node) {
    return (
      <div style={{
        width: 280, flexShrink: 0, borderLeft: '1px solid rgba(0,180,216,0.1)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        padding: 24, textAlign: 'center',
      }}>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11.5, color: 'var(--text-muted, #8492a6)' }}>
          Select a role on the canvas to edit its details.
        </span>
      </div>
    )
  }

  const isRoot = node.parentId == null

  const commitTitle = () => {
    if (title !== (node.title || '')) onPatch({ title })
  }

  const commitLabel = () => {
    if (!isRoot && label !== (node.type || '')) onPatch({ type: label })
  }

  const commitResponsibilities = (next) => {
    setResponsibilities(next)
    onPatch({ responsibilities: next })
  }

  const commitRename = () => {
    const trimmed = renameValue.trim()
    if (trimmed && trimmed !== node.id) onPatch({ _renameId: trimmed })
    setRenaming(false)
  }


  // Provider override — always sends the whole merged object (Go's patch
  // replaces `provider` wholesale, no deep-patching); `overrides` carries
  // whichever field just changed, since its own setState hasn't landed yet
  // when the commit fires.
  const commitProvider = (overrides) => {
    const merged = cleanObj({
      kind: pKind, vendor: pVendor, apiKeyEnv: pApiKeyEnv, baseUrl: pBaseUrl, authTokenEnv: pAuthTokenEnv,
      ...overrides,
    })
    onPatch({ provider: Object.keys(merged).length ? merged : null })
  }
  const clearProvider = () => {
    setPKind(''); setPVendor(''); setPApiKeyEnv(''); setPBaseUrl(''); setPAuthTokenEnv('')
    onPatch({ provider: null })
  }

  // Tool policy — same whole-object-replace shape as provider, so every key
  // the saved policy carries has to be resent, including the ones this panel
  // has no control for (`approvalTools`, which internal/orggrant's reconcile
  // manages, and anything monomind or a hand edit added). Dropping them here
  // would silently strip them on the next field change.
  const commitPolicy = (overrides) => {
    const merged = cleanObj({
      git: policyGit,
      maxTokens: numOrUndefined(policyMaxTokens),
      maxUsd: numOrUndefined(policyMaxUsd),
      allowTools, denyTools, fileWrite, fileRead, webAllow, autoApproveTools,
      ...overrides,
    })
    onPatch({ policy: { ...omit(node.rest?.policy, MODELED_POLICY_FIELDS), ...merged } })
  }
  const clearPolicy = () => {
    setPolicyGit('read'); setPolicyMaxTokens(''); setPolicyMaxUsd('')
    setAllowTools([]); setDenyTools([]); setFileWrite([]); setFileRead([]); setWebAllow([]); setAutoApproveTools([])
    onPatch({ policy: null })
  }

  const otherRoles = (allNodes || []).filter(n => n.id !== node.id && !wouldCycle(allNodes, node.id, n.id))
  const advancedRest = omit(node.rest, MODELED_REST_FIELDS)
  const policyListKey = `${syncedId}:${policyResetGen}`
  const isEndpoint = node.rest?.kind === 'endpoint'
  const showAutomations = !!orgName

  // Plan §8 layer 8: a role holding grants should not keep Bash.
  const denyBash = () => {
    if (denyTools.includes('Bash')) return
    const next = [...denyTools, 'Bash']
    setDenyTools(next)
    commitPolicy({ denyTools: next })
  }

  return (
    <div style={{
      width: 280, flexShrink: 0, borderLeft: '1px solid rgba(0,180,216,0.1)',
      overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 16, padding: 14,
      fontFamily: 'var(--font-mono)',
    }}>
      {/* Identity */}
      <section>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
          <button
            onClick={onOpenIconPicker}
            title="Change icon"
            style={{ padding: 0, border: '2px solid rgba(0,180,216,0.3)', borderRadius: '50%', cursor: 'pointer', background: 'transparent', flexShrink: 0 }}
          >
            <img src={iconUrl(node.icon || 'coder')} alt="" style={{ width: 44, height: 44, borderRadius: '50%', display: 'block' }} />
          </button>
          <div style={{ flex: 1, minWidth: 0 }}>
            <input
              className="form-input"
              value={title}
              onChange={e => setTitle(e.target.value)}
              onBlur={commitTitle}
              onKeyDown={e => { if (e.key === 'Enter') e.currentTarget.blur() }}
              placeholder="Role title"
              style={{ fontSize: 13, padding: '6px 8px' }}
            />
          </div>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 10 }}>
          {!renaming ? (
            <>
              <span style={{ fontSize: 10.5, color: 'var(--text-muted, #8492a6)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1 }}>
                id: {node.id}
              </span>
              <button
                onClick={() => setRenaming(true)}
                style={{ background: 'none', border: 'none', color: '#00b4d8', cursor: 'pointer', fontSize: 10, fontFamily: 'var(--font-mono)', padding: 0 }}
              >
                Rename id...
              </button>
            </>
          ) : (
            <div style={{ display: 'flex', gap: 4, width: '100%' }}>
              <input
                className="form-input"
                autoFocus
                value={renameValue}
                onChange={e => setRenameValue(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') commitRename(); if (e.key === 'Escape') setRenaming(false) }}
                style={{ fontSize: 11, padding: '4px 6px', flex: 1 }}
              />
              <button className="btn btn-sm btn-primary" onClick={commitRename}>OK</button>
              <button className="btn btn-sm btn-ghost" onClick={() => setRenaming(false)}>Cancel</button>
            </div>
          )}
        </div>

        {/* "boss" is derived from tree position (see orgdesign.Doc.PromoteToRoot)
            — the root role always IS the boss, never a separately-typed
            label, so this is a fact display for root and a one-click action
            for everyone else, not another text field to fill in. */}
        {isRoot ? (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 6, padding: '5px 10px',
            borderRadius: 6, background: 'rgba(234,179,8,0.1)', border: '1px solid rgba(234,179,8,0.35)',
            fontSize: 10.5, color: 'var(--yellow)',
          }}>
            <Crown size={12} color="var(--yellow)" fill="var(--yellow)" />
            Org boss (entry point)
          </div>
        ) : (
          <button
            onClick={onPromoteToRoot}
            title="Make this role the org's entry point — the current boss becomes its direct report"
            style={{
              display: 'flex', alignItems: 'center', gap: 6, padding: '5px 10px', width: '100%',
              borderRadius: 6, background: 'transparent', border: '1px solid var(--border, rgba(255,255,255,0.12))',
              fontSize: 10.5, color: 'var(--text-secondary, #8899aa)', cursor: 'pointer', fontFamily: 'var(--font-mono)',
            }}
          >
            <Crown size={12} />
            Set as org boss
          </button>
        )}
      </section>

      {/* Label (type) — free-form and purely cosmetic (feeds the agent's
          system-prompt line); no fixed options, since nothing in the org
          runtime branches on it except the literal string "boss", which is
          handled structurally above, not here. */}
      {!isRoot && (
        <section>
          <div className="form-label">Label</div>
          <input
            className="form-input"
            value={label}
            onChange={e => setLabel(e.target.value)}
            onBlur={commitLabel}
            onKeyDown={e => { if (e.key === 'Enter') e.currentTarget.blur() }}
            placeholder="specialist, researcher, reviewer, or anything descriptive"
            style={{ fontSize: 11 }}
          />
        </section>
      )}

      {/* Responsibilities */}
      <StringListField
        label="Responsibilities"
        values={responsibilities}
        onChange={commitResponsibilities}
        resetKey={syncedId}
        placeholder='e.g. "Review pull requests for security issues"'
        addLabel="+ Add responsibility"
      />

      {showAutomations && (
        <RoleAutomationsSection
          orgName={orgName}
          node={node}
          grants={grants}
          automations={automations}
          engineOffline={engineOffline}
          bashDenied={denyTools.includes('Bash')}
          onDenyBash={denyBash}
          onChanged={onGrantsChanged}
          onOpenWorkflow={onOpenWorkflow}
          onEditGrant={onEditGrant}
        />
      )}

      {/* An automation role has no session, so no runtime, model, or policy
          (monomind validation rejects them on endpoint roles). */}
      {!isEndpoint && (<>
      <RoleFullAccessSection
        orgName={orgName}
        roleID={node.id}
        entry={fullAccess}
        declared={declaresFull(node)}
        runtime={node.rest?.runtime || orgRuntime || 'claude'}
        onChanged={onAccessChanged}
      />

      {/* Runtime */}
      <section>
        <div className="form-label">Runtime</div>
        <RuntimeSelect
          value={node.rest?.runtime ?? ''}
          options={RUNTIME_OPTIONS}
          sectionsOrg={sectionsOrg}
          onChange={runtime => onPatch({ runtime })}
        />
      </section>

      {/* Model / provider name / budgets */}
      <ModelBudgetsSection model={model} setModel={setModel} providerName={providerName} setProviderName={setProviderName} maxTurns={maxTurns} setMaxTurns={setMaxTurns} budgetTokens={budgetTokens} setBudgetTokens={setBudgetTokens} budgetUsd={budgetUsd} setBudgetUsd={setBudgetUsd} onPatch={onPatch} />

      {/* Provider override — the inline block, distinct from "provider name"
          above; wins over it when both are set. */}
      <ProviderOverrideSection pKind={pKind} setPKind={setPKind} pVendor={pVendor} setPVendor={setPVendor} pApiKeyEnv={pApiKeyEnv} setPApiKeyEnv={setPApiKeyEnv} pBaseUrl={pBaseUrl} setPBaseUrl={setPBaseUrl} pAuthTokenEnv={pAuthTokenEnv} setPAuthTokenEnv={setPAuthTokenEnv} clearProvider={clearProvider} commitProvider={commitProvider} />

      {/* Tool policy */}
      <ToolPolicySection policyGit={policyGit} setPolicyGit={setPolicyGit} policyMaxTokens={policyMaxTokens} setPolicyMaxTokens={setPolicyMaxTokens} policyMaxUsd={policyMaxUsd} setPolicyMaxUsd={setPolicyMaxUsd} allowTools={allowTools} setAllowTools={setAllowTools} denyTools={denyTools} setDenyTools={setDenyTools} fileWrite={fileWrite} setFileWrite={setFileWrite} fileRead={fileRead} setFileRead={setFileRead} webAllow={webAllow} setWebAllow={setWebAllow} autoApproveTools={autoApproveTools} setAutoApproveTools={setAutoApproveTools} policyListKey={policyListKey} clearPolicy={clearPolicy} commitPolicy={commitPolicy} />

      {showAutomations && <EffectiveTools orgName={orgName} roleId={node.id} refreshKey={`${grantsVersion ?? 0}:${policyKey}`} />}

      {/* Instructions file */}
      <InstructionsFileSection instructionsFile={instructionsFile} setInstructionsFile={setInstructionsFile} onPatch={onPatch} />
      </>)}

      {/* Reports to */}
      <section>
        <div className="form-label">Reports to</div>
        <select
          className="form-select"
          value={node.parentId ?? ''}
          onChange={e => onSetReportsTo(e.target.value === '' ? null : e.target.value)}
        >
          <option value="">— make this the root —</option>
          {otherRoles.map(n => <option key={n.id} value={n.id}>{n.title || n.id}</option>)}
        </select>
      </section>

      {/* Everything else */}
      {advancedRest && Object.keys(advancedRest).length > 0 && (
        <section>
          <div className="form-label">Advanced (read-only)</div>
          <KVBlock obj={advancedRest} />
        </section>
      )}

      <button className="btn btn-danger" onClick={onDelete} style={{ marginTop: 4 }}>
        Delete role
      </button>
    </div>
  )
}

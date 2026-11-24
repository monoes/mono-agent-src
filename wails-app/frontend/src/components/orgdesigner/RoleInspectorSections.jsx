import StringListField from '../StringListField.jsx'
import { api } from '../../services/api.js'
import { PROVIDER_KIND_OPTIONS, PROVIDER_VENDOR_OPTIONS, GIT_LEVEL_OPTIONS, numOrUndefined } from './roleInspectorModel.js'

// Form sections of RoleInspector. State stays in RoleInspector; these only render.

export function ModelBudgetsSection({ model, setModel, providerName, setProviderName, maxTurns, setMaxTurns, budgetTokens, setBudgetTokens, budgetUsd, setBudgetUsd, onPatch }) {
  return (
      <section>
        <div className="form-label">Model / budgets</div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <input
            className="form-input"
            value={model}
            onChange={e => setModel(e.target.value)}
            onBlur={() => onPatch({ model: model === '' ? undefined : model })}
            placeholder="claude-sonnet-4-5"
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            value={providerName}
            onChange={e => setProviderName(e.target.value)}
            onBlur={() => onPatch({ adapter_config_provider: providerName === '' ? undefined : providerName })}
            placeholder="provider name (from `monomind providers configure`)"
            title="References a provider already configured with `monomind providers configure`. Distinct from the Provider override section below, which defines one inline — an inline override wins if both are set."
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            type="number"
            value={maxTurns}
            onChange={e => setMaxTurns(e.target.value)}
            onBlur={() => onPatch({ max_turns_per_message: numOrUndefined(maxTurns) })}
            placeholder="max turns per message"
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            type="number"
            value={budgetTokens}
            onChange={e => setBudgetTokens(e.target.value)}
            onBlur={() => onPatch({ budget_tokens: numOrUndefined(budgetTokens) })}
            placeholder="budget (tokens)"
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            type="number"
            value={budgetUsd}
            onChange={e => setBudgetUsd(e.target.value)}
            onBlur={() => onPatch({ budget_usd: numOrUndefined(budgetUsd) })}
            placeholder="budget (USD)"
            style={{ fontSize: 11 }}
          />
        </div>
      </section>
  )
}

export function ProviderOverrideSection({ pKind, setPKind, pVendor, setPVendor, pApiKeyEnv, setPApiKeyEnv, pBaseUrl, setPBaseUrl, pAuthTokenEnv, setPAuthTokenEnv, clearProvider, commitProvider }) {
  return (
      <section>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div className="form-label" style={{ marginBottom: 0 }}>Provider override</div>
          {(pKind || pVendor || pApiKeyEnv || pBaseUrl || pAuthTokenEnv) && (
            <button
              onClick={clearProvider}
              style={{ background: 'none', border: 'none', color: 'var(--text-muted, #8492a6)', cursor: 'pointer', fontSize: 9.5, fontFamily: 'var(--font-mono)', padding: 0 }}
            >
              Clear
            </button>
          )}
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 6 }}>
          <select className="form-select" value={pKind} onChange={e => { setPKind(e.target.value); commitProvider({ kind: e.target.value || undefined }) }}>
            <option value="">(none)</option>
            {PROVIDER_KIND_OPTIONS.map(k => <option key={k} value={k}>{k}</option>)}
          </select>
          <select className="form-select" value={pVendor} onChange={e => { setPVendor(e.target.value); commitProvider({ vendor: e.target.value || undefined }) }}>
            <option value="">(vendor)</option>
            {PROVIDER_VENDOR_OPTIONS.map(v => <option key={v} value={v}>{v}</option>)}
          </select>
          <input
            className="form-input"
            value={pApiKeyEnv}
            onChange={e => setPApiKeyEnv(e.target.value)}
            onBlur={() => commitProvider({ apiKeyEnv: pApiKeyEnv || undefined })}
            placeholder="API key env var name"
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            value={pBaseUrl}
            onChange={e => setPBaseUrl(e.target.value)}
            onBlur={() => commitProvider({ baseUrl: pBaseUrl || undefined })}
            placeholder="base URL (for base-url kind)"
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            value={pAuthTokenEnv}
            onChange={e => setPAuthTokenEnv(e.target.value)}
            onBlur={() => commitProvider({ authTokenEnv: pAuthTokenEnv || undefined })}
            placeholder="auth token env var name"
            style={{ fontSize: 11 }}
          />
        </div>
      </section>
  )
}

export function ToolPolicySection({
  policyGit, setPolicyGit, policyMaxTokens, setPolicyMaxTokens, policyMaxUsd, setPolicyMaxUsd,
  allowTools, setAllowTools, denyTools, setDenyTools, fileWrite, setFileWrite, fileRead, setFileRead,
  webAllow, setWebAllow, autoApproveTools, setAutoApproveTools, policyListKey, clearPolicy, commitPolicy,
}) {
  return (
      <section>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div className="form-label" style={{ marginBottom: 0 }}>Tool policy</div>
          <button
            onClick={clearPolicy}
            style={{ background: 'none', border: 'none', color: 'var(--text-muted, #8492a6)', cursor: 'pointer', fontSize: 9.5, fontFamily: 'var(--font-mono)', padding: 0 }}
          >
            Clear
          </button>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 6 }}>
          <select className="form-select" value={policyGit} onChange={e => { setPolicyGit(e.target.value); commitPolicy({ git: e.target.value }) }}>
            {GIT_LEVEL_OPTIONS.map(g => <option key={g} value={g}>git: {g}</option>)}
          </select>
          <input
            className="form-input"
            type="number"
            value={policyMaxTokens}
            onChange={e => setPolicyMaxTokens(e.target.value)}
            onBlur={() => commitPolicy({ maxTokens: numOrUndefined(policyMaxTokens) })}
            placeholder="max tokens (this role's own cap)"
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            type="number"
            value={policyMaxUsd}
            onChange={e => setPolicyMaxUsd(e.target.value)}
            onBlur={() => commitPolicy({ maxUsd: numOrUndefined(policyMaxUsd) })}
            placeholder="max USD (this role's own cap)"
            style={{ fontSize: 11 }}
          />
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 10 }}>
          <StringListField label="Allow tools" values={allowTools} onChange={v => { setAllowTools(v); commitPolicy({ allowTools: v }) }} resetKey={policyListKey} addLabel="+ Add tool" />
          <StringListField label="Deny tools" values={denyTools} onChange={v => { setDenyTools(v); commitPolicy({ denyTools: v }) }} resetKey={policyListKey} addLabel="+ Add tool" />
          <StringListField label="File write globs" values={fileWrite} onChange={v => { setFileWrite(v); commitPolicy({ fileWrite: v }) }} resetKey={policyListKey} placeholder="default: **" addLabel="+ Add glob" />
          <StringListField label="File read globs" values={fileRead} onChange={v => { setFileRead(v); commitPolicy({ fileRead: v }) }} resetKey={policyListKey} placeholder="default: **" addLabel="+ Add glob" />
          <StringListField label="Web allow (hosts)" values={webAllow} onChange={v => { setWebAllow(v); commitPolicy({ webAllow: v }) }} resetKey={policyListKey} placeholder="unset: default web access" addLabel="+ Add host" />
          <StringListField label="Auto-approve tools" values={autoApproveTools} onChange={v => { setAutoApproveTools(v); commitPolicy({ autoApproveTools: v }) }} resetKey={policyListKey} placeholder="skips the human-approval pause" addLabel="+ Add tool" />
        </div>
      </section>
  )
}

export function InstructionsFileSection({ instructionsFile, setInstructionsFile, onPatch }) {
  return (
      <section>
        <div className="form-label">Instructions file</div>
        <div style={{ display: 'flex', gap: 4 }}>
          <input
            className="form-input"
            value={instructionsFile}
            onChange={e => setInstructionsFile(e.target.value)}
            onBlur={() => onPatch({ instructions_file: instructionsFile })}
            placeholder="path to a .md/.txt file"
            style={{ fontSize: 11, flex: 1 }}
          />
          <button
            className="btn btn-sm btn-secondary"
            onClick={async () => {
              const path = await api.chooseInstructionsFile()
              if (path) {
                setInstructionsFile(path)
                onPatch({ instructions_file: path })
              }
            }}
          >
            Browse…
          </button>
        </div>
        <div style={{ fontSize: 9.5, color: 'var(--text-dim, #6c7b90)', marginTop: 4 }}>
          Saved with the role, but not yet read by the org runtime — reserved for a future release.
        </div>
      </section>
  )
}

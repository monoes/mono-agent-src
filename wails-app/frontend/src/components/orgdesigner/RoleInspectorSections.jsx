import StringListField from '../StringListField.jsx'
import { api } from '../../services/api.js'
import { PROVIDER_KIND_OPTIONS, PROVIDER_VENDOR_OPTIONS, GIT_LEVEL_OPTIONS, numOrUndefined } from './roleInspectorModel.js'
import { useTranslation } from 'react-i18next'

// Form sections of RoleInspector. State stays in RoleInspector; these only render.

export function ModelBudgetsSection({ model, setModel, providerName, setProviderName, maxTurns, setMaxTurns, budgetTokens, setBudgetTokens, budgetUsd, setBudgetUsd, onPatch }) {
  const { t } = useTranslation()
  return (
      <section>
        <div className="form-label">{t('ui6OrgsMore.model_budgets')}</div>
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
            placeholder={t('ui6OrgsMore.provider_name')}
            title={t('ui6OrgsMore.provider_name_title')}
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            type="number"
            value={maxTurns}
            onChange={e => setMaxTurns(e.target.value)}
            onBlur={() => onPatch({ max_turns_per_message: numOrUndefined(maxTurns) })}
            placeholder={t('ui6OrgsMore.max_turns')}
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            type="number"
            value={budgetTokens}
            onChange={e => setBudgetTokens(e.target.value)}
            onBlur={() => onPatch({ budget_tokens: numOrUndefined(budgetTokens) })}
            placeholder={t('ui6OrgsMore.budget_tokens')}
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            type="number"
            value={budgetUsd}
            onChange={e => setBudgetUsd(e.target.value)}
            onBlur={() => onPatch({ budget_usd: numOrUndefined(budgetUsd) })}
            placeholder={t('ui6OrgsMore.budget_usd')}
            style={{ fontSize: 11 }}
          />
        </div>
      </section>
  )
}

export function ProviderOverrideSection({ pKind, setPKind, pVendor, setPVendor, pApiKeyEnv, setPApiKeyEnv, pBaseUrl, setPBaseUrl, pAuthTokenEnv, setPAuthTokenEnv, clearProvider, commitProvider }) {
  const { t } = useTranslation()
  return (
      <section>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div className="form-label" style={{ marginBottom: 0 }}>{t('ui6OrgsMore.provider_override')}</div>
          {(pKind || pVendor || pApiKeyEnv || pBaseUrl || pAuthTokenEnv) && (
            <button
              onClick={clearProvider}
              style={{ background: 'none', border: 'none', color: 'var(--text-muted, #8492a6)', cursor: 'pointer', fontSize: 9.5, fontFamily: 'var(--font-mono)', padding: 0 }}
            >
              {t('ui6OrgsMore.clear')}
            </button>
          )}
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 6 }}>
          <select className="form-select" value={pKind} onChange={e => { setPKind(e.target.value); commitProvider({ kind: e.target.value || undefined }) }}>
            <option value="">{t('ui6OrgsMore.none')}</option>
            {PROVIDER_KIND_OPTIONS.map(k => <option key={k} value={k}>{k}</option>)}
          </select>
          <select className="form-select" value={pVendor} onChange={e => { setPVendor(e.target.value); commitProvider({ vendor: e.target.value || undefined }) }}>
            <option value="">{t('ui6OrgsMore.vendor')}</option>
            {PROVIDER_VENDOR_OPTIONS.map(v => <option key={v} value={v}>{v}</option>)}
          </select>
          <input
            className="form-input"
            value={pApiKeyEnv}
            onChange={e => setPApiKeyEnv(e.target.value)}
            onBlur={() => commitProvider({ apiKeyEnv: pApiKeyEnv || undefined })}
            placeholder={t('ui6OrgsMore.api_key_env')}
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            value={pBaseUrl}
            onChange={e => setPBaseUrl(e.target.value)}
            onBlur={() => commitProvider({ baseUrl: pBaseUrl || undefined })}
            placeholder={t('ui6OrgsMore.base_url')}
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            value={pAuthTokenEnv}
            onChange={e => setPAuthTokenEnv(e.target.value)}
            onBlur={() => commitProvider({ authTokenEnv: pAuthTokenEnv || undefined })}
            placeholder={t('ui6OrgsMore.auth_token_env')}
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
  const { t } = useTranslation()
  return (
      <section>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div className="form-label" style={{ marginBottom: 0 }}>{t('ui6OrgsMore.tool_policy')}</div>
          <button
            onClick={clearPolicy}
            style={{ background: 'none', border: 'none', color: 'var(--text-muted, #8492a6)', cursor: 'pointer', fontSize: 9.5, fontFamily: 'var(--font-mono)', padding: 0 }}
          >
            {t('ui6OrgsMore.clear')}
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
            placeholder={t('ui6OrgsMore.policy_max_tokens')}
            style={{ fontSize: 11 }}
          />
          <input
            className="form-input"
            type="number"
            value={policyMaxUsd}
            onChange={e => setPolicyMaxUsd(e.target.value)}
            onBlur={() => commitPolicy({ maxUsd: numOrUndefined(policyMaxUsd) })}
            placeholder={t('ui6OrgsMore.policy_max_usd')}
            style={{ fontSize: 11 }}
          />
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 10 }}>
          <StringListField label={t('ui6OrgsMore.allow_tools')} values={allowTools} onChange={v => { setAllowTools(v); commitPolicy({ allowTools: v }) }} resetKey={policyListKey} addLabel={t('ui6OrgsMore.add_tool')} />
          <StringListField label={t('ui6OrgsMore.deny_tools')} values={denyTools} onChange={v => { setDenyTools(v); commitPolicy({ denyTools: v }) }} resetKey={policyListKey} addLabel={t('ui6OrgsMore.add_tool')} />
          <StringListField label={t('ui6OrgsMore.write_globs')} values={fileWrite} onChange={v => { setFileWrite(v); commitPolicy({ fileWrite: v }) }} resetKey={policyListKey} placeholder={t('ui6OrgsMore.default_globs')} addLabel={t('ui6OrgsMore.add_glob')} />
          <StringListField label={t('ui6OrgsMore.read_globs')} values={fileRead} onChange={v => { setFileRead(v); commitPolicy({ fileRead: v }) }} resetKey={policyListKey} placeholder={t('ui6OrgsMore.default_globs')} addLabel={t('ui6OrgsMore.add_glob')} />
          <StringListField label={t('ui6OrgsMore.web_allow')} values={webAllow} onChange={v => { setWebAllow(v); commitPolicy({ webAllow: v }) }} resetKey={policyListKey} placeholder={t('ui6OrgsMore.web_placeholder')} addLabel={t('ui6OrgsMore.add_host')} />
          <StringListField label={t('ui6OrgsMore.auto_approve')} values={autoApproveTools} onChange={v => { setAutoApproveTools(v); commitPolicy({ autoApproveTools: v }) }} resetKey={policyListKey} placeholder={t('ui6OrgsMore.auto_approve_placeholder')} addLabel={t('ui6OrgsMore.add_tool')} />
        </div>
      </section>
  )
}

export function InstructionsFileSection({ instructionsFile, setInstructionsFile, onPatch }) {
  const { t } = useTranslation()
  return (
      <section>
        <div className="form-label">{t('ui6OrgsMore.instructions_file')}</div>
        <div style={{ display: 'flex', gap: 4 }}>
          <input
            className="form-input"
            value={instructionsFile}
            onChange={e => setInstructionsFile(e.target.value)}
            onBlur={() => onPatch({ instructions_file: instructionsFile })}
            placeholder={t('ui6OrgsMore.instructions_path')}
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
            {t('ui6OrgsMore.browse')}
          </button>
        </div>
        <div style={{ fontSize: 9.5, color: 'var(--text-dim, #6c7b90)', marginTop: 4 }}>
          {t('ui6OrgsMore.instructions_note')}
        </div>
      </section>
  )
}

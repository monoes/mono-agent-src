export const RUNTIME_OPTIONS = [
  'claude', 'kimicode', 'opencode', 'vercel', 'codex', 'antigravity',
  'grok', 'qwen', 'crush', 'copilot', 'pi', 'pi-rpc', 'qwen-rpc',
  'cline', 'aider', 'dsh',
]
export const PROVIDER_KIND_OPTIONS = [
  'subscription', 'api-key', 'base-url', 'bedrock', 'vertex', 'vercel-api-key', 'codex', 'antigravity',
]
export const PROVIDER_VENDOR_OPTIONS = [
  'openai', 'anthropic', 'google', 'xai', 'deepseek', 'glm', 'mistral', 'groq',
  'together', 'fireworks', 'cohere', 'perplexity', 'alibaba', 'openrouter', 'ollama', 'openai-compatible',
]
export const GIT_LEVEL_OPTIONS = ['none', 'read', 'commit', 'push']
// Fields this component surfaces via dedicated controls even though
// orgGraph.js's hydrate() does not (yet) hoist them to top-level canvas
// node properties — they currently live in `node.rest`. Read from there,
// patched by wire field name, and excluded from the read-only Advanced
// KVBlock below so they aren't shown twice. `adapter_config` (not `model`)
// is the real wire key — the role's model/provider-name/max_tokens live
// nested under it, never as a flat top-level field.
// The automation keys are shown by RoleAutomationsSection instead; `endpoint`
// holds a capability URL, so it is never dumped into the Advanced block.
export const MODELED_REST_FIELDS = [
  'runtime', 'adapter_config', 'max_turns_per_message', 'budget_tokens', 'budget_usd',
  'policy', 'provider', 'instructions_file',
  'kind', 'endpoint', 'automation', 'automations',
]
// The `policy` keys this panel has controls for. Everything else in a saved
// policy is preserved verbatim by commitPolicy, since a role patch replaces
// `policy` wholesale.
export const MODELED_POLICY_FIELDS = [
  'git', 'maxTokens', 'maxUsd',
  'allowTools', 'denyTools', 'fileWrite', 'fileRead', 'webAllow', 'autoApproveTools',
]

export function omit(obj, keys) {
  if (!obj) return obj
  const out = {}
  Object.keys(obj).forEach(k => { if (!keys.includes(k)) out[k] = obj[k] })
  return out
}

// Drop keys whose value is '' or nullish — used before sending a merged
// object-patch (provider/policy) so unset fields don't get written as
// empty strings, while still allowing arrays (including empty ones, which
// are a meaningful "explicitly none" distinct from "unset") through as-is.
export function cleanObj(obj) {
  const out = {}
  Object.entries(obj).forEach(([k, v]) => {
    if (Array.isArray(v) || (v !== '' && v != null)) out[k] = v
  })
  return out
}

export const numOrUndefined = (v) => (v === '' || v === null || v === undefined ? undefined : Number(v))

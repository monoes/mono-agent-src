// Whether the agent turn behind a workflow node's result ran without a sandbox.
// The CLI decides: these are monomind.SandboxMissing's verdicts, under the item
// key each node that runs turns writes. Another node can pass such an item on,
// so only the node that ran the turn counts.
const MISSING = new Set(['unsupported', 'awaiting-monomind', 'needs-monomind', 'off'])

const VERDICT_KEY = {
  'agent.ask': '_agent_sandbox',
  'ai.guardrails': '_agent_sandbox',
  'ai.extract_page': '_agent_sandbox',
  'applications.evaluate': '_agent_sandbox',
  'core.evaluation': '_agent_sandbox',
  'browser.jev': '_text_agent_sandbox',
}

// ranUnsandboxed reads node.runOutputs, whose items are flat after a test run
// and {json} when read from a stored execution.
export function ranUnsandboxed(node) {
  const key = VERDICT_KEY[node?.subtype]
  if (!key) return false
  // Its other operations pass the incoming items on.
  if (node.subtype === 'core.evaluation' && node.config?.operation !== 'setMetrics') return false
  return (node.runOutputs || []).some(o => o.items?.some(i => MISSING.has(i?.[key] ?? i?.json?.[key])))
}

// Display names for runtime ids that don't read as a product name on their
// own. Every other runtime shows its id (claude, codex, cline, …).
const RUNTIME_LABELS = {
  dsh: 'DeepSeek Harness',
  kilo: 'Kilo Code',
}

// runtimeLabel is how a runtime id reads in pickers and lists.
export function runtimeLabel(id) {
  return RUNTIME_LABELS[id] || id
}

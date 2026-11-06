// How a Stop result (the control room's {ok, stopped, output, detail}) is read. One reading for the chat header's Stop and the
// control room's Stop panel, so they never disagree about whether a run is stopped.

// stopLine is the verdict line of the command's output ("stopped", or what the CLI says instead).
export function stopLine(output) {
  const lines = String(output || '').split('\n').map(l => l.trim()).filter(Boolean)
  return lines.find(l => /^(stopped|NOT stopped|Stop )/.test(l)) || lines[0] || ''
}

// transcriptMessage is the message the CLI wrote into this chat's transcript, from its own "transcript <chat>: <message>"
// line; '' when it wrote none (the chat has no session of the run).
export function transcriptMessage(output, conversationId) {
  const prefix = `transcript ${conversationId}: `
  const line = String(output || '').split('\n').find(l => l.startsWith(prefix))
  return line ? line.slice(prefix.length) : ''
}

// isRepair: a later stop that found no open run and finished what the previous one left (exit 0, no "stopped" first line).
export function isRepair(output) {
  return /^no open run; ended sessions left open by the previous stop/.test(String(output || '').trim())
}

// A line the CLI printed about something it could not do: shown as a warning, never as a success.
export function isWarnLine(line) {
  return /\bNOT (ended|written|stopped|RECORDED)\b|cannot be read|^Stop /.test(line)
}

// outcome classifies a Stop result: 'ok' (stopped, or a later stop finished the previous one's cleanup), 'warn' (the run IS
// stopped but the CLI could not finish its sessions or message: exit 8, first line still "stopped"), 'fail' (anything else).
export function outcome(result) {
  const out = String(result?.output || '').trim()
  if (result?.ok && (result?.stopped || isRepair(out))) return 'ok'
  if (!result?.ok && /^(stopped|no open run; (ended sessions|the previous stop))/.test(out)) return 'warn'
  return 'fail'
}

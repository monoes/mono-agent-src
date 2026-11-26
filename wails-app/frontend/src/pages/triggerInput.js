// Trigger-input helpers for the run dialog — UI concerns only.
//
// Which fields a workflow reads, and what examples to offer for them, is not
// decided here: it comes from `monoagentcli workflow inputs --json` (see
// internal/workflow/trigger_inputs.go), so a script, a test and this dialog
// all get the same answer. What is left below is presentation: validating
// what the user typed, and remembering it between runs.

import i18n from '../i18n.js';

// parseTriggerInput validates what the user typed. Returns { ok, value } or
// { ok: false, error } with a message worth showing.
export function parseTriggerInput(text) {
  const trimmed = (text || '').trim();
  if (trimmed === '' || trimmed === '{}') return { ok: true, value: '' };
  let parsed;
  try {
    parsed = JSON.parse(trimmed);
  } catch (err) {
    return { ok: false, error: i18n.t('ui6Node.notJson', { error: err.message }) };
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { ok: false, error: i18n.t('ui6Node.triggerMustBeObject') };
  }
  return { ok: true, value: JSON.stringify(parsed) };
}

// Trigger data is remembered per workflow so a re-run starts from what worked
// last time rather than an empty skeleton. Per-viewer convenience only —
// wrapped because storage can be unavailable or throw.
const REMEMBER_PREFIX = 'monoagent:wf-trigger-input:';

export function rememberTriggerInput(workflowId, inputJSON) {
  if (!workflowId) return;
  try {
    if (inputJSON) localStorage.setItem(REMEMBER_PREFIX + workflowId, inputJSON);
    else localStorage.removeItem(REMEMBER_PREFIX + workflowId);
  } catch {
    // storage unavailable — remembering is optional
  }
}

// rememberedTriggerInput is what the dialog opens with: what the user ran
// last time for this workflow, else the skeleton the CLI supplied.
export function rememberedTriggerInput(workflowId, skeleton) {
  try {
    const saved = workflowId && localStorage.getItem(REMEMBER_PREFIX + workflowId);
    if (saved) return JSON.stringify(JSON.parse(saved), null, 2);
  } catch {
    // unreadable or malformed — fall through to the fresh skeleton
  }
  return JSON.stringify(skeleton ?? {}, null, 2);
}

// ── Form view of the trigger data ─────────────────────────────────────────
// The run dialog shows one labelled field per key the workflow reads (from
// `workflow inputs --json`), with the raw JSON one toggle away. A field the
// CLI marks structured (read through `json`, i.e. a collection) — or one
// whose current value is not a string — is edited as JSON; any other is
// plain text.

// formFromJSON splits trigger JSON text into per-field form state, or
// returns null when the text is not a JSON object (the raw view keeps it).
export function formFromJSON(text, fields) {
  let obj;
  try {
    obj = JSON.parse((text || '').trim() || '{}');
  } catch {
    return null;
  }
  if (obj === null || typeof obj !== 'object' || Array.isArray(obj)) return null;
  const values = {};
  const asJSON = {};
  const extra = { ...obj };
  for (const f of fields) {
    const v = obj[f.name];
    delete extra[f.name];
    asJSON[f.name] = !!f.structured || (v !== undefined && typeof v !== 'string');
    values[f.name] = v === undefined ? '' : asJSON[f.name] ? JSON.stringify(v, null, 2) : v;
  }
  return { values, asJSON, extra };
}

// jsonFromForm is the inverse: { ok, text } with the trigger JSON (pretty),
// or { ok: false, error } naming the field whose JSON does not parse. Empty
// fields are left out, as an unset key; keys the form does not show are kept.
export function jsonFromForm(form, fields) {
  const out = { ...form.extra };
  for (const f of fields) {
    const raw = form.values[f.name] ?? '';
    if (String(raw).trim() === '') continue;
    if (!form.asJSON[f.name]) {
      out[f.name] = raw;
      continue;
    }
    try {
      out[f.name] = JSON.parse(raw);
    } catch (err) {
      return { ok: false, error: i18n.t('ui6Node.fieldNotJson', { name: f.name, error: err.message }) };
    }
  }
  return { ok: true, text: JSON.stringify(out, null, 2) };
}

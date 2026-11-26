import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../../services/api.js'
import i18n from '../../i18n.js'

// `org validate`'s report for the designer (#205): monomind's own checks,
// including full-access taint problems (a full-access role that reads
// untrusted input, or is reachable from one via reports_to). Its "error" is
// one problem per line; the designer shows them next to its structural
// checks.

// A role that declares full access without a human grant on file. monomind
// reports it only as a line of the text output (warnings stays empty):
//   growth: role builder: no human acknowledgement on file — run `monomind org role set-access <org> <role> full`
const UNACKNOWLEDGED = /role ([^\s:]+): no human acknowledgement on file[^\n]*/g

// unacknowledgedRoles maps role id → that line, from a report's output and
// error text.
export function unacknowledgedRoles(report) {
  const out = {}
  const text = `${report?.output || ''}\n${report?.error || ''}`
  for (const m of text.matchAll(UNACKNOWLEDGED)) out[m[1]] = m[0].trim()
  return out
}

// reportProblems splits a report into { errors, warnings, unacknowledged }.
export function reportProblems(report) {
  const unacknowledged = unacknowledgedRoles(report)
  if (!report || report.valid !== false) return { errors: [], warnings: report?.warnings || [], unacknowledged }
  const errors = String(report.error || i18n.t('ui6OrgsDesigner.invalid_config'))
    .split('\n').map(l => l.trim().replace(/^[-•]\s*/, '')).filter(Boolean)
  return { errors, warnings: report.warnings || [], unacknowledged }
}

// useOrgValidateReport runs `org validate` for orgName, again whenever stamp
// changes (debounced) or refresh() is called (after a grant, which changes
// no config). A failed call contributes nothing: the structural
// checks still stand, and a save is validated by the CLI regardless.
export function useOrgValidateReport(orgName, stamp = '') {
  const [problems, setProblems] = useState({ errors: [], warnings: [], unacknowledged: {} })
  const [nonce, setNonce] = useState(0)
  const refresh = useCallback(() => setNonce(n => n + 1), [])
  const current = useRef(orgName)
  current.current = orgName
  useEffect(() => {
    if (!orgName) { setProblems({ errors: [], warnings: [], unacknowledged: {} }); return }
    const name = orgName
    const id = setTimeout(() => {
      Promise.resolve().then(() => api.validateOrgReport(name))
        .then(report => { if (current.current === name) setProblems(reportProblems(report)) })
        .catch(() => { if (current.current === name) setProblems({ errors: [], warnings: [], unacknowledged: {} }) })
    }, stamp && !nonce ? 600 : 0)
    return () => clearTimeout(id)
  }, [orgName, stamp, nonce])
  return { ...problems, refresh }
}

// mergeValidation adds the CLI's errors (not already reported by the
// structural checks) to validateStructure's { valid, errors }.
export function mergeValidation(structural, cli) {
  const known = [...structural.errors, ...(structural.english || [])]
  const extra = (cli?.errors || []).filter(e => !known.includes(e))
  return {
    ...structural,
    valid: structural.valid && extra.length === 0,
    errors: [...structural.errors, ...extra],
    cliErrors: extra,
    warnings: cli?.warnings || [],
  }
}
